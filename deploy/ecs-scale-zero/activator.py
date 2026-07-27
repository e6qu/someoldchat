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
IDLE_AFTER = int(os.environ["IDLE_AFTER"])
MAX_BODY_BYTES = int(os.environ["MAX_BODY_BYTES"])
# The `scale-down` item is shared with cmd/ecs-ws-activator, which holds it for
# scaleDownLockTTL (15s) and documents why the expiry must be short: the release
# is a conditional delete that can fail on a throttle or a killed task, and while
# the item is unexpired *both* activators refuse every new request lease and
# every new WebSocket lease. This side used to write REQUEST_TIMEOUT + 60 = 85s
# on the same item, so a sweep killed by the Lambda timeout blocked the whole
# deployment for 85 seconds. The sweep now bounds its own work by this value so
# the lock cannot outlive the work it protects.
#
# It is not "one shared setting", and saying so was wrong: cmd/ecs-ws-activator's
# scaleDownLockTTL is a Go constant with no flag, so the two halves can only be
# kept equal by pinning this one. deploy/ecs-scale-zero/variables.tf pins it to
# 15 and records what has to change on the Go side to make it settable again.
SCALE_DOWN_LOCK_SECONDS = int(os.environ["SCALE_DOWN_LOCK_SECONDS"])
SCALE_DOWN_LOCK = "scale-down"
LAST_REQUEST = "last-request"
# Response headers whose value is per-hop or recomputed by API Gateway. Set-Cookie
# is excluded from the header map because duplicates cannot survive a mapping;
# it is returned through the payload format 2.0 `cookies` list instead.
HOP_BY_HOP_RESPONSE_HEADERS = {"connection", "content-length", "transfer-encoding", "set-cookie"}
# Request headers whose value is per-hop or recomputed for the upstream call.
HOP_BY_HOP_REQUEST_HEADERS = {"host", "content-length", "connection", "transfer-encoding"}


def request_headers(event):
    """Rebuilds the upstream request headers, including the session cookie.

    API Gateway HTTP API payload format 2.0 delivers the request's cookies in a
    top-level `cookies` array, not reliably as a `cookie` header — the exact
    asymmetry the response direction already accounts for. Reading only
    `headers` therefore forwarded every request without the SameOldChat session
    cookie, so `internal/web` saw an anonymous request and the application behind
    this Lambda was permanently signed out: sign-in appeared to succeed and then
    bounced straight back to the login page.
    """
    headers = {k: v for k, v in (event.get("headers") or {}).items() if k.lower() not in HOP_BY_HOP_REQUEST_HEADERS}
    cookies = [cookie for cookie in (event.get("cookies") or []) if cookie]
    if not cookies:
        return headers
    # A configuration that also leaves `cookie` in the header map must not lose
    # either source, and a cookie must not be sent twice.
    existing = [name for name in headers if name.lower() == "cookie"]
    present = []
    for name in existing:
        present.extend(pair.strip() for pair in headers.pop(name).split(";") if pair.strip())
    for cookie in cookies:
        if cookie not in present:
            present.append(cookie)
    headers["cookie"] = "; ".join(present)
    return headers


def response(status, error, retry_after=None):
    """Answers a refusal with the Slack error envelope, not plain text.

    docs/operations.md states that a request the activator cannot serve receives
    "the closest compatible Slack error envelope recorded in the compatibility
    ledger". This path answered `text/plain`, so an official Slack SDK raised a
    JSON decode error instead of surfacing a Slack error code — which is the
    entire point of that claim.
    """
    headers = {"content-type": "application/json; charset=utf-8"}
    if retry_after is not None:
        headers["retry-after"] = str(retry_after)
    body = json.dumps({"ok": False, "error": error}, separators=(",", ":"))
    return {"statusCode": status, "headers": headers, "body": body, "isBase64Encoded": False}


def running_tasks():
    pages = ecs.get_paginator("list_tasks").paginate(cluster=CLUSTER, desiredStatus="RUNNING", family=TASK_FAMILY)
    arns = [arn for page in pages for arn in page.get("taskArns", [])]
    if not arns:
        return []
    tasks = []
    for start in range(0, len(arns), 100):
        tasks.extend(ecs.describe_tasks(cluster=CLUSTER, tasks=arns[start:start + 100]).get("tasks", []))
    return tasks


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
    """Releases the wake lock, reporting rather than raising on failure.

    This ran in a `finally` after `tasks` had been populated, so a
    ConditionalCheckFailedException from the conditional delete escaped the inner
    `try`, was caught by the outer handler, and turned a wake that had actually
    succeeded into a 503. The lock carries an `expires` attribute, so a skipped
    release is reclaimed by the next expiry rather than leaking, which is the
    same reasoning the bottom `finally` already records.
    """
    try:
        STATE.delete_item(Key={"id": "wake"}, ConditionExpression="owner = :owner", ExpressionAttributeValues={":owner": owner})
    except ClientError as error:
        print(json.dumps({"cleanup_error": "release wake lock: " + str(error)}))


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
    # The lease key is an unguessable per-invocation UUID, so possession of the
    # key is itself the ownership proof and no owner condition is needed. A
    # delete that finds nothing is the already-expired case, which is benign.
    STATE.delete_item(Key={"id": name})


def note_request():
    """Starts (or restarts) the idle window at the current instant.

    The row's own `expires` attribute is the end of the window, so an absent or
    expired row means the window has elapsed. Time-to-live removal is eventually
    consistent, so `expires` is always compared explicitly rather than relied on.
    """
    at = int(time.time())
    STATE.put_item(Item={"id": LAST_REQUEST, "at": at, "expires": at + IDLE_AFTER})


def within_idle_window(now):
    item = STATE.get_item(Key={"id": LAST_REQUEST}, ConsistentRead=True).get("Item")
    if not item:
        return False
    return int(item.get("expires", 0)) > now


def stop_if_idle():
    """Stops application tasks only once the configured idle window has elapsed.

    This used to run in every request's `finally` and stopped every task as soon
    as no other lease was held, so there was no idle window at all and each
    non-overlapping request paid a full Fargate cold start. The idle window is
    now enforced here and the scheduled maintenance invocation is what actually
    reaches the stop, because the request that starts the window can never also
    be the invocation that closes it.
    """
    now = int(time.time())
    # The lock excludes every new request and WebSocket lease while this runs, so
    # the work must not outlive it. Each step checks the same deadline and gives
    # up rather than continuing past the expiry another activator is entitled to
    # take the lock at.
    deadline = time.time() + SCALE_DOWN_LOCK_SECONDS
    lock_owner = "scale-down:" + str(uuid.uuid4())
    try:
        STATE.put_item(
            Item={"id": SCALE_DOWN_LOCK, "owner": lock_owner, "expires": now + SCALE_DOWN_LOCK_SECONDS},
            ConditionExpression="attribute_not_exists(id) OR expires < :now",
            ExpressionAttributeValues={":now": now},
        )
    except ClientError as error:
        if error.response.get("Error", {}).get("Code") == "ConditionalCheckFailedException":
            return
        raise ActivationError("unable to acquire scale-down lock") from error
    try:
        if within_idle_window(int(time.time())):
            return
        leases = []
        scan = {"ConsistentRead": True, "FilterExpression": "begins_with(id, :prefix) AND expires > :now", "ExpressionAttributeValues": {":prefix": "lease:", ":now": int(time.time())}}
        while True:
            if time.time() >= deadline:
                raise ActivationError("scale-down lease scan exceeded the scale-down lock lifetime; the next sweep retries")
            page = STATE.scan(**scan)
            leases.extend(page.get("Items", []))
            last_key = page.get("LastEvaluatedKey")
            if not last_key:
                break
            scan["ExclusiveStartKey"] = last_key
        if leases:
            return
        for task in running_tasks():
            if time.time() >= deadline:
                raise ActivationError("scale-down exceeded the scale-down lock lifetime before every task was stopped; the next sweep finishes it")
            ecs.stop_task(cluster=CLUSTER, task=task["taskArn"], reason="scale-to-zero request complete")
    finally:
        try:
            STATE.delete_item(Key={"id": SCALE_DOWN_LOCK}, ConditionExpression="owner = :owner", ExpressionAttributeValues={":owner": lock_owner})
        except ClientError as error:
            # The item carries `expires`, so a failed release lapses on its own
            # within SCALE_DOWN_LOCK_SECONDS. Raising here would replace the real
            # outcome of the sweep with a cleanup failure.
            print(json.dumps({"cleanup_error": "release scale-down lock: " + str(error)}))


def maintenance():
    """Closes the idle window without a request in flight.

    A scheduled invocation is the only thing that can observe an elapsed idle
    window, so `aws_cloudwatch_event_rule.activator_idle_sweep` drives this.
    """
    try:
        stop_if_idle()
    except (ActivationError, ClientError) as error:
        print(json.dumps({"maintenance_error": str(error)}))
        return {"stopped": False}
    return {"stopped": True}


def handler(event, _context):
    if (event or {}).get("sameoldchat_maintenance"):
        return maintenance()
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
            return response(503, "service_unavailable", retry_after=1)
        request_id = event.get("requestContext", {}).get("requestId", str(uuid.uuid4()))
        index = int(hashlib.sha256(request_id.encode()).hexdigest(), 16) % len(tasks)
        _, ip = tasks[index]
        raw_path = event.get("rawPath", "/")
        query = event.get("rawQueryString", "")
        target = f"http://{ip}:{PORT}{raw_path}" + (f"?{query}" if query else "")
        headers = request_headers(event)
        body = event.get("body") or ""
        if event.get("isBase64Encoded"):
            body = base64.b64decode(body)
        else:
            body = body.encode()
        # The Go activator rejects an oversized body with 413 before it reaches
        # the application (internal/activator/handler.go:137). This path had no
        # cap at all, so the two activators enforced different contracts.
        if len(body) > MAX_BODY_BYTES:
            return response(413, "request_entity_too_large")
        # An absent method must never be inferred: defaulting to GET turned a
        # mutating call into a read and answered it as a success.
        method = event.get("requestContext", {}).get("http", {}).get("method")
        if not method:
            raise ActivationError("request event carries no HTTP method")
        remaining = request_deadline - time.time()
        if remaining <= 1:
            return response(503, "service_unavailable", retry_after=1)
        result = http.request(method, target, body=body, headers=headers, timeout=urllib3.Timeout(connect=min(2.0, remaining), read=remaining), retries=False, preload_content=True)
        response_body = base64.b64encode(result.data).decode()
        # dict() over a urllib3 HTTPHeaderDict comma-joins repeated keys, which
        # is correct for Vary or Cache-Control but destroys Set-Cookie: an
        # Expires attribute contains a comma, so a joined pair cannot be split
        # again and the browser kept at most one cookie. Payload format 2.0 has
        # a dedicated cookies list for exactly this.
        response_headers = {k: v for k, v in dict(result.headers).items() if k.lower() not in HOP_BY_HOP_RESPONSE_HEADERS}
        payload = {"statusCode": result.status, "headers": response_headers, "body": response_body, "isBase64Encoded": True}
        cookies = result.headers.getlist("Set-Cookie")
        if cookies:
            payload["cookies"] = cookies
        return payload
    except (ActivationError, ClientError, urllib3.exceptions.HTTPError, ValueError) as error:
        print(json.dumps({"error": str(error)}))
        return response(503, "service_unavailable", retry_after=1)
    finally:
        # `finally` runs after the return value has been computed, so an
        # exception escaping here discarded a completed 200 and failed the
        # Lambda, which API Gateway reports as 500/502. AGENTS.md:48-50 forbids
        # turning a handled error into an HTTP 500. Every lease and lock carries
        # an `expires` attribute, so a skipped cleanup step is reclaimed by the
        # next expiry instead of leaking, which is why swallowing is safe here
        # and is not hiding a broken primary path.
        #
        # Scale-down is deliberately absent here: the request that restarts the
        # idle window can never be the invocation that observes it elapsed, so
        # attempting it would only take the scale-down lock and return.
        for step in (note_request, lambda: release(lease_id)):
            try:
                step()
            except Exception as error:  # noqa: BLE001
                print(json.dumps({"cleanup_error": str(error)}))
