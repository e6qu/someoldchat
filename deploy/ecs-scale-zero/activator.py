import base64
import hashlib
import json
import os
import time
import uuid

import boto3
import urllib3
from botocore.exceptions import ClientError

ecs = boto3.client("ecs")
dynamodb = boto3.resource("dynamodb")
http = urllib3.PoolManager(cert_reqs="CERT_REQUIRED")


class ActivationError(RuntimeError):
    pass

CLUSTER = os.environ["CLUSTER"]
TASK_DEFINITION = os.environ["TASK_DEFINITION"]
TASK_FAMILY = TASK_DEFINITION.rsplit("/", 1)[-1].split(":", 1)[0]
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
    pages = ecs.get_paginator("list_tasks").paginate(cluster=CLUSTER, desiredStatus="RUNNING", family=TASK_FAMILY)
    arns = [arn for page in pages for arn in page.get("taskArns", [])]
    if not arns:
        return []
    return ecs.describe_tasks(cluster=CLUSTER, tasks=arns).get("tasks", [])


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
    result = ecs.run_task(cluster=CLUSTER, taskDefinition=TASK_DEFINITION, count=REPLICAS, launchType="FARGATE", platformVersion="1.4.0", networkConfiguration={"awsvpcConfiguration": {"subnets": SUBNETS, "securityGroups": SECURITY_GROUPS, "assignPublicIp": "DISABLED"}}, enableECSManagedTags=True, tags=[{"key": "sameoldchat-scale-zero", "value": "true"}])
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
        leases = []
        scan = {"ConsistentRead": True, "FilterExpression": "begins_with(id, :prefix) AND expires > :now", "ExpressionAttributeValues": {":prefix": "lease:", ":now": int(time.time())}}
        while True:
            page = STATE.scan(**scan)
            leases.extend(page.get("Items", []))
            last_key = page.get("LastEvaluatedKey")
            if not last_key:
                break
            scan["ExclusiveStartKey"] = last_key
        if leases:
            return
        for task in running_tasks():
            ecs.stop_task(cluster=CLUSTER, task=task["taskArn"], reason="scale-to-zero request complete")
    finally:
        STATE.delete_item(Key={"id": SCALE_DOWN_LOCK}, ConditionExpression="owner = :owner", ExpressionAttributeValues={":owner": lock_owner})


def handler(event, _context):
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
        request_id = event.get("requestContext", {}).get("requestId", str(uuid.uuid4()))
        index = int(hashlib.sha256(request_id.encode()).hexdigest(), 16) % len(tasks)
        _, ip = tasks[index]
        raw_path = event.get("rawPath", "/")
        query = event.get("rawQueryString", "")
        target = f"http://{ip}:{PORT}{raw_path}" + (f"?{query}" if query else "")
        headers = {k: v for k, v in (event.get("headers") or {}).items() if k.lower() not in {"host", "content-length", "connection", "transfer-encoding"}}
        body = event.get("body") or ""
        if event.get("isBase64Encoded"):
            body = base64.b64decode(body)
        else:
            body = body.encode()
        remaining = request_deadline - time.time()
        if remaining <= 1:
            return response(503, "application startup consumed the request deadline\n", retry_after=1)
        result = http.request(event.get("requestContext", {}).get("http", {}).get("method", "GET"), target, body=body, headers=headers, timeout=urllib3.Timeout(connect=min(2.0, remaining), read=remaining), retries=False, preload_content=True)
        response_body = base64.b64encode(result.data).decode()
        response_headers = {k: v for k, v in dict(result.headers).items() if k.lower() not in {"connection", "content-length", "transfer-encoding"}}
        return {"statusCode": result.status, "headers": response_headers, "body": response_body, "isBase64Encoded": True}
    except (ActivationError, ClientError, urllib3.exceptions.HTTPError, ValueError) as error:
        print(json.dumps({"error": str(error)}))
        return response(503, "activator could not serve the request\n", retry_after=1)
    finally:
        release(lease_id)
        stop_if_idle()
