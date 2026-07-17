import base64
import hashlib
import json
import os
import time
import uuid
from typing import NamedTuple

import boto3
import urllib3
from botocore.exceptions import ClientError

ecs = boto3.client("ecs")
dynamodb = boto3.resource("dynamodb")
http = urllib3.PoolManager(cert_reqs="CERT_REQUIRED")


class ActivationError(RuntimeError):
    pass


class GatewayRequest(NamedTuple):
    request_id: str
    method: str
    raw_path: str
    raw_query_string: str
    headers: dict[str, str]
    body: str
    is_base64_encoded: bool


def parse_gateway_request(event):
    if not isinstance(event, dict):
        raise ActivationError("API Gateway event must be an object")
    request_context = event.get("requestContext")
    if not isinstance(request_context, dict):
        raise ActivationError("API Gateway event is missing requestContext")
    request_id = request_context.get("requestId")
    http_context = request_context.get("http")
    if not isinstance(request_id, str) or not request_id.strip():
        raise ActivationError("API Gateway event is missing requestContext.requestId")
    if not isinstance(http_context, dict):
        raise ActivationError("API Gateway event is missing requestContext.http")
    method = http_context.get("method")
    raw_path = event.get("rawPath")
    if not isinstance(method, str) or not method.strip():
        raise ActivationError("API Gateway event is missing requestContext.http.method")
    if not isinstance(raw_path, str) or not raw_path.startswith("/"):
        raise ActivationError("API Gateway event is missing an absolute rawPath")
    if "rawQueryString" not in event:
        raise ActivationError("API Gateway event is missing rawQueryString")
    raw_query_string = event["rawQueryString"]
    if not isinstance(raw_query_string, str):
        raise ActivationError("API Gateway event rawQueryString must be a string")
    if "headers" not in event:
        raise ActivationError("API Gateway event is missing headers")
    headers = event["headers"]
    if not isinstance(headers, dict) or any(not isinstance(key, str) or not isinstance(value, str) for key, value in headers.items()):
        raise ActivationError("API Gateway event headers must be a string map")
    if "body" not in event:
        raise ActivationError("API Gateway event is missing body")
    body = event["body"]
    if body is None:
        body = ""
    if not isinstance(body, str):
        raise ActivationError("API Gateway event body must be a string or null")
    if "isBase64Encoded" not in event:
        raise ActivationError("API Gateway event is missing isBase64Encoded")
    is_base64_encoded = event["isBase64Encoded"]
    if not isinstance(is_base64_encoded, bool):
        raise ActivationError("API Gateway event isBase64Encoded must be boolean")
    return GatewayRequest(request_id, method, raw_path, raw_query_string, headers, body, is_base64_encoded)


CLUSTER = os.environ["CLUSTER"]
TASK_DEFINITION = os.environ["TASK_DEFINITION"]
STARTED_BY = os.environ["STARTED_BY"]
SUBNETS = os.environ["SUBNETS"].split(",")
SECURITY_GROUPS = os.environ["SECURITY_GROUPS"].split(",")
PORT = int(os.environ["PORT"])
REPLICAS = int(os.environ["REPLICAS"])
STARTUP_TIMEOUT = int(os.environ["STARTUP_TIMEOUT"])
REQUEST_TIMEOUT = int(os.environ["REQUEST_TIMEOUT"])
STATE = dynamodb.Table(os.environ["STATE_TABLE"])
STATE_CLIENT = dynamodb.meta.client
SCALE_DOWN_LOCK = "scale-down"


def response(status, body, retry_after=None):
    headers = {"content-type": "text/plain; charset=utf-8"}
    if retry_after is not None:
        headers["retry-after"] = str(retry_after)
    return {"statusCode": status, "headers": headers, "body": body, "isBase64Encoded": False}


def running_tasks():
    pages = ecs.get_paginator("list_tasks").paginate(cluster=CLUSTER, startedBy=STARTED_BY)
    tasks = []
    for batch in task_arn_batches(pages, 100):
        tasks.extend(ecs.describe_tasks(cluster=CLUSTER, tasks=batch).get("tasks", []))
    return tasks


def task_arn_batches(pages, size):
    if size <= 0:
        raise ValueError("batch size must be positive")
    batch = []
    for page in pages:
        for arn in page.get("taskArns", []):
            batch.append(arn)
            if len(batch) == size:
                yield batch
                batch = []
    if batch:
        yield batch


def task_ip(task):
    for attachment in task.get("attachments", []):
        if attachment.get("type") != "ElasticNetworkInterface":
            continue
        details = {item["name"]: item["value"] for item in attachment.get("details", [])}
        if details.get("privateIPv4Address"):
            return details["privateIPv4Address"]
    return None


def ready_tasks(deadline, wait_for_tasks=False):
    while time.time() < deadline:
        tasks = running_tasks()
        if not tasks:
            if not wait_for_tasks:
                return []
            time.sleep(0.25)
            continue
        ready = []
        for task in tasks:
            ip = task_ip(task)
            if not ip:
                continue
            try:
                check = http.request("GET", f"http://{ip}:{PORT}/readyz", timeout=urllib3.Timeout(connect=1.0, read=1.0), retries=False)
            except urllib3.exceptions.HTTPError:
                continue
            if check.status == 200:
                ready.append((task["taskArn"], ip))
        if ready:
            return ready
        time.sleep(0.25)
    return []


def acquire_wake_lock(owner):
    try:
        STATE.put_item(
            Item={"id": "wake", "owner": owner, "expires": int(time.time()) + STARTUP_TIMEOUT + 30},
            ConditionExpression="attribute_not_exists(id) OR expires < :now",
            ExpressionAttributeValues={":now": int(time.time())},
        )
        return True
    except ClientError as error:
        if error.response.get("Error", {}).get("Code") == "ConditionalCheckFailedException":
            return False
        raise ActivationError("unable to acquire activator wake lock") from error


def release_wake_lock(owner):
    STATE.delete_item(Key={"id": "wake"}, ConditionExpression="owner = :owner", ExpressionAttributeValues={":owner": owner})


def start_tasks():
    result = ecs.run_task(cluster=CLUSTER, taskDefinition=TASK_DEFINITION, count=REPLICAS, startedBy=STARTED_BY, launchType="FARGATE", platformVersion="1.4.0", networkConfiguration={"awsvpcConfiguration": {"subnets": SUBNETS, "securityGroups": SECURITY_GROUPS, "assignPublicIp": "DISABLED"}}, enableECSManagedTags=True, tags=[{"key": "sameoldchat-scale-zero", "value": "true"}])
    failures = result.get("failures", [])
    if failures:
        raise ActivationError("ECS RunTask failed: " + json.dumps(failures, separators=(",", ":")))


def lease(name):
    try:
        now = int(time.time())
        STATE_CLIENT.transact_write_items(
            TransactItems=[
                {
                    "ConditionCheck": {
                        "TableName": STATE.table_name,
                        "Key": {"id": {"S": SCALE_DOWN_LOCK}},
                        "ConditionExpression": "attribute_not_exists(id) OR expires < :now",
                        "ExpressionAttributeValues": {":now": {"N": str(now)}},
                    }
                },
                {
                    "Put": {
                        "TableName": STATE.table_name,
                        "Item": {"id": {"S": name}, "expires": {"N": str(now + REQUEST_TIMEOUT + 60)}},
                        "ConditionExpression": "attribute_not_exists(id)",
                    }
                },
            ]
        )
    except ClientError as error:
        raise ActivationError("unable to acquire activator lease") from error


def release(name):
    STATE.delete_item(Key={"id": name})


def has_active_lease():
    scan = {"ConsistentRead": True, "FilterExpression": "begins_with(id, :prefix) AND expires > :now", "ExpressionAttributeValues": {":prefix": "lease:", ":now": int(time.time())}}
    while True:
        page = STATE.scan(**scan)
        if page.get("Items"):
            return True
        last_key = page.get("LastEvaluatedKey")
        if not last_key:
            return False
        scan["ExclusiveStartKey"] = last_key


def stop_if_idle():
    now = int(time.time())
    lock_owner = "scale-down:" + str(uuid.uuid4())
    try:
        STATE.put_item(
            Item={"id": SCALE_DOWN_LOCK, "owner": lock_owner, "expires": now + REQUEST_TIMEOUT + 60},
            ConditionExpression="attribute_not_exists(id) OR expires < :now",
            ExpressionAttributeValues={":now": now},
        )
    except ClientError as error:
        if error.response.get("Error", {}).get("Code") == "ConditionalCheckFailedException":
            return
        raise ActivationError("unable to acquire scale-down lock") from error
    try:
        if has_active_lease():
            return
        for task in running_tasks():
            ecs.stop_task(cluster=CLUSTER, task=task["taskArn"], reason="scale-to-zero request complete")
    finally:
        STATE.delete_item(Key={"id": SCALE_DOWN_LOCK}, ConditionExpression="owner = :owner", ExpressionAttributeValues={":owner": lock_owner})


def handler(event, _context):
    request = parse_gateway_request(event)
    lease_id = "lease:" + str(uuid.uuid4())
    wake_owner = lease_id
    request_deadline = time.time() + REQUEST_TIMEOUT
    try:
        lease(lease_id)
        startup_deadline = min(request_deadline, time.time() + STARTUP_TIMEOUT)
        tasks = ready_tasks(startup_deadline)
        if not tasks:
            if acquire_wake_lock(wake_owner):
                try:
                    tasks = ready_tasks(min(request_deadline, time.time() + STARTUP_TIMEOUT))
                    if not tasks:
                        start_tasks()
                    tasks = ready_tasks(min(request_deadline, time.time() + STARTUP_TIMEOUT), wait_for_tasks=True)
                finally:
                    release_wake_lock(wake_owner)
            else:
                tasks = ready_tasks(min(request_deadline, time.time() + STARTUP_TIMEOUT), wait_for_tasks=True)
        if not tasks:
            return response(503, "application did not become ready\n", retry_after=1)
        index = int(hashlib.sha256(request.request_id.encode()).hexdigest(), 16) % len(tasks)
        _, ip = tasks[index]
        target = f"http://{ip}:{PORT}{request.raw_path}" + (f"?{request.raw_query_string}" if request.raw_query_string else "")
        headers = {k: v for k, v in request.headers.items() if k.lower() not in {"host", "content-length", "connection", "transfer-encoding"}}
        if request.is_base64_encoded:
            body = base64.b64decode(request.body)
        else:
            body = request.body.encode()
        remaining = request_deadline - time.time()
        if remaining <= 1:
            return response(503, "application startup consumed the request deadline\n", retry_after=1)
        result = http.request(request.method, target, body=body, headers=headers, timeout=urllib3.Timeout(connect=min(2.0, remaining), read=remaining), retries=False, preload_content=True)
        response_body = base64.b64encode(result.data).decode()
        response_headers = {k: v for k, v in dict(result.headers).items() if k.lower() not in {"connection", "content-length", "transfer-encoding"}}
        return {"statusCode": result.status, "headers": response_headers, "body": response_body, "isBase64Encoded": True}
    except (ActivationError, ClientError, urllib3.exceptions.HTTPError, ValueError) as error:
        print(json.dumps({"error": str(error)}))
        return response(503, "activator could not serve the request\n", retry_after=1)
    finally:
        release(lease_id)
        stop_if_idle()
