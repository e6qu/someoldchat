"""Contract tests for the scale-to-zero HTTP activator Lambda.

The module is deployment-critical (`main.tf` zips `activator.py` verbatim and
runs it as the public front door) and had no automated coverage at all, so the
defects these tests pin down were invisible to every repository gate. `boto3`
and `urllib3` are replaced by stubs before import, so the tests need no AWS
credentials, no network, and no third-party packages.

Run with: python3 deploy/ecs-scale-zero/activator_test.py
"""

import base64
import importlib
import json
import os
import sys
import types
import unittest


class StubHeaders:
    """The subset of urllib3.HTTPHeaderDict the activator relies on.

    `dict()` over the real type comma-joins repeated keys; that behaviour is
    reproduced here because it is precisely what broke Set-Cookie.
    """

    def __init__(self, pairs):
        self._pairs = list(pairs)

    def keys(self):
        seen = []
        for key, _ in self._pairs:
            if key.lower() not in [existing.lower() for existing in seen]:
                seen.append(key)
        return seen

    def __getitem__(self, key):
        values = [value for name, value in self._pairs if name.lower() == key.lower()]
        if not values:
            raise KeyError(key)
        return ", ".join(values)

    def items(self):
        return [(key, self[key]) for key in self.keys()]

    def getlist(self, key):
        return [value for name, value in self._pairs if name.lower() == key.lower()]


class StubResponse:
    def __init__(self, status, data=b"", headers=()):
        self.status = status
        self.data = data
        self.headers = StubHeaders(headers)


class StubTable:
    def __init__(self, name="state"):
        self.table_name = name
        self.items = {}
        self.calls = []
        self.put_item_error = None
        self.delete_item_error = None

    def put_item(self, Item, **_kwargs):  # noqa: N803 - boto3 keyword spelling
        self.calls.append(("put_item", Item["id"]))
        if self.put_item_error is not None:
            raise self.put_item_error
        self.items[Item["id"]] = dict(Item)

    def get_item(self, Key, **_kwargs):  # noqa: N803 - boto3 keyword spelling
        item = self.items.get(Key["id"])
        return {"Item": dict(item)} if item is not None else {}

    def delete_item(self, Key, **_kwargs):  # noqa: N803 - boto3 keyword spelling
        self.calls.append(("delete_item", Key["id"]))
        if self.delete_item_error is not None:
            raise self.delete_item_error
        self.items.pop(Key["id"], None)

    def scan(self, **_kwargs):
        leases = [item for key, item in self.items.items() if key.startswith("lease:")]
        return {"Items": leases}


class StubECS:
    def __init__(self):
        self.stopped = []
        self.started = 0
        self.tasks = []

    def get_paginator(self, _name):
        outer = self

        class Paginator:
            def paginate(self, **_kwargs):
                return [{"taskArns": [task["taskArn"] for task in outer.tasks]}]

        return Paginator()

    def describe_tasks(self, **_kwargs):
        return {"tasks": self.tasks}

    def stop_task(self, cluster, task, reason):  # noqa: ARG002 - boto3 signature
        self.stopped.append(task)

    def run_task(self, **_kwargs):
        self.started += 1
        return {"failures": []}


def install_stubs():
    """Replaces boto3 and urllib3 so `activator` imports without AWS or network."""

    class StubClientError(Exception):
        def __init__(self, response, operation="Op"):
            super().__init__(operation)
            self.response = response

    table = StubTable()
    ecs = StubECS()
    requests = []

    class Timeout:
        def __init__(self, connect=None, read=None):
            self.connect = connect
            self.read = read

    class HTTPError(Exception):
        pass

    class PoolManager:
        """Answers readiness probes itself and records only forwarded requests."""

        def __init__(self, **_kwargs):
            self.responder = None
            self.ready = True

        def request(self, method, url, **kwargs):
            if url.endswith("/readyz"):
                return StubResponse(200 if self.ready else 503)
            requests.append({"method": method, "url": url, **kwargs})
            if self.responder is None:
                return StubResponse(200)
            return self.responder(method, url, kwargs)

    urllib3 = types.ModuleType("urllib3")
    urllib3.PoolManager = PoolManager
    urllib3.Timeout = Timeout
    urllib3.exceptions = types.SimpleNamespace(HTTPError=HTTPError)

    boto3 = types.ModuleType("boto3")

    class Resource:
        def __init__(self):
            self.meta = types.SimpleNamespace(client=types.SimpleNamespace(transact_write_items=self._transact))

        def Table(self, _name):  # noqa: N802 - boto3 spelling
            return table

        def _transact(self, TransactItems):  # noqa: N803 - boto3 spelling
            for entry in TransactItems:
                put = entry.get("Put")
                if put is not None:
                    identifier = put["Item"]["id"]["S"]
                    table.items[identifier] = {"id": identifier, "expires": int(put["Item"]["expires"]["N"])}

    boto3.client = lambda _name: ecs
    boto3.resource = lambda _name: Resource()

    botocore = types.ModuleType("botocore")
    exceptions = types.ModuleType("botocore.exceptions")
    exceptions.ClientError = StubClientError
    botocore.exceptions = exceptions

    sys.modules.update({"boto3": boto3, "urllib3": urllib3, "botocore": botocore, "botocore.exceptions": exceptions})
    return table, ecs, requests, StubClientError


ENVIRONMENT = {
    "CLUSTER": "chat",
    "TASK_DEFINITION": "arn:aws:ecs:eu-west-1:1:task-definition/chat:7",
    "SUBNETS": "subnet-a",
    "SECURITY_GROUPS": "sg-a",
    "PORT": "8080",
    "REPLICAS": "1",
    "STARTUP_TIMEOUT": "20",
    "REQUEST_TIMEOUT": "25",
    "STATE_TABLE": "chat-activator",
    "IDLE_AFTER": "1800",
    "MAX_BODY_BYTES": "4194304",
}


class ActivatorTest(unittest.TestCase):
    def setUp(self):
        os.environ.update(ENVIRONMENT)
        sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
        self.table, self.ecs, self.requests, self.ClientError = install_stubs()
        sys.modules.pop("activator", None)
        self.activator = importlib.import_module("activator")
        self.ecs.tasks = [
            {
                "taskArn": "arn:task/1",
                "attachments": [
                    {
                        "type": "ElasticNetworkInterface",
                        "details": [{"name": "privateIPv4Address", "value": "10.0.0.7"}],
                    }
                ],
            }
        ]

    def request_event(self, method="GET", body=None, base64_encoded=False):
        event = {
            "rawPath": "/login",
            "rawQueryString": "",
            "headers": {"host": "example.test"},
            "requestContext": {"requestId": "r1", "http": {"method": method}},
        }
        if body is not None:
            event["body"] = body
            event["isBase64Encoded"] = base64_encoded
        return event

    def test_duplicate_set_cookie_headers_survive_as_a_cookies_list(self):
        """Regression: dict(HTTPHeaderDict) comma-joined them into one unsplittable header."""
        self.activator.http.responder = lambda *_: StubResponse(
            200,
            b"ok",
            [
                ("Content-Type", "text/html"),
                ("Set-Cookie", "sid=abc; Path=/; Expires=Wed, 21 Oct 2026 07:28:00 GMT"),
                ("Set-Cookie", "csrf=def; Path=/"),
                ("Vary", "Accept"),
                ("Vary", "Cookie"),
            ],
        )
        result = self.activator.handler(self.request_event(), None)

        self.assertEqual(result["statusCode"], 200)
        self.assertEqual(
            result["cookies"],
            ["sid=abc; Path=/; Expires=Wed, 21 Oct 2026 07:28:00 GMT", "csrf=def; Path=/"],
        )
        self.assertNotIn("Set-Cookie", result["headers"])
        # Joining is still correct for list-valued headers that permit it.
        self.assertEqual(result["headers"]["Vary"], "Accept, Cookie")

    def test_a_cleanup_failure_does_not_discard_a_successful_response(self):
        """Regression: an exception from the finally block failed the Lambda, so API Gateway returned 500."""
        self.activator.http.responder = lambda *_: StubResponse(200, b"ok", [("Content-Type", "text/plain")])
        self.table.delete_item_error = self.ClientError({"Error": {"Code": "ConditionalCheckFailedException"}})
        self.table.put_item_error = None

        result = self.activator.handler(self.request_event(), None)

        self.assertEqual(result["statusCode"], 200)
        self.assertEqual(base64.b64decode(result["body"]), b"ok")

    def test_a_request_does_not_stop_the_application(self):
        """Regression: stop_if_idle ran in every request's finally, so each request was a cold start."""
        self.activator.http.responder = lambda *_: StubResponse(200, b"ok")

        self.activator.handler(self.request_event(), None)

        self.assertEqual(self.ecs.stopped, [])
        self.assertIn(self.activator.LAST_REQUEST, self.table.items)

    def test_the_idle_sweep_waits_for_the_idle_window(self):
        self.activator.http.responder = lambda *_: StubResponse(200, b"ok")
        self.activator.handler(self.request_event(), None)

        self.assertEqual(self.activator.handler({"sameoldchat_maintenance": True}, None), {"stopped": True})
        self.assertEqual(self.ecs.stopped, [], "the window has not elapsed yet")

        marker = self.table.items[self.activator.LAST_REQUEST]
        marker["expires"] = marker["at"] - 1

        self.assertEqual(self.activator.handler({"sameoldchat_maintenance": True}, None), {"stopped": True})
        self.assertEqual(self.ecs.stopped, ["arn:task/1"], "the window elapsed, so tasks stop")

    def test_an_open_lease_blocks_the_idle_sweep(self):
        self.table.items["lease:other"] = {"id": "lease:other", "expires": 2 ** 40}

        self.activator.handler({"sameoldchat_maintenance": True}, None)

        self.assertEqual(self.ecs.stopped, [])

    def test_an_absent_method_is_rejected_instead_of_becoming_a_get(self):
        event = self.request_event()
        del event["requestContext"]["http"]["method"]

        result = self.activator.handler(event, None)

        self.assertEqual(result["statusCode"], 503)
        self.assertEqual(self.requests, [], "no upstream call is made for an unusable event")

    def test_an_oversized_body_is_rejected_with_413_like_the_go_activator(self):
        oversized = base64.b64encode(b"x" * (int(ENVIRONMENT["MAX_BODY_BYTES"]) + 1)).decode()

        result = self.activator.handler(self.request_event("POST", oversized, base64_encoded=True), None)

        self.assertEqual(result["statusCode"], 413)
        self.assertEqual(self.requests, [])

    def test_a_body_at_the_limit_is_forwarded(self):
        self.activator.http.responder = lambda *_: StubResponse(204)
        allowed = base64.b64encode(b"x" * int(ENVIRONMENT["MAX_BODY_BYTES"])).decode()

        result = self.activator.handler(self.request_event("POST", allowed, base64_encoded=True), None)

        self.assertEqual(result["statusCode"], 204)
        self.assertEqual(self.requests[-1]["method"], "POST")

    def test_the_upstream_method_and_target_are_forwarded_verbatim(self):
        self.activator.http.responder = lambda *_: StubResponse(201)

        self.activator.handler(self.request_event("DELETE"), None)

        self.assertEqual(self.requests[-1]["method"], "DELETE")
        self.assertEqual(self.requests[-1]["url"], "http://10.0.0.7:8080/login")

    def test_the_terraform_module_supplies_every_required_environment_variable(self):
        """A missing variable would only surface as a 502 on the first cold start."""
        module = os.path.join(os.path.dirname(os.path.abspath(__file__)), "main.tf")
        with open(module, encoding="utf-8") as handle:
            body = handle.read()
        source = os.path.join(os.path.dirname(os.path.abspath(__file__)), "activator.py")
        with open(source, encoding="utf-8") as handle:
            code = handle.read()
        required = sorted({node for node in _environ_keys(code)})
        missing = [name for name in required if name + " =" not in body]
        self.assertEqual(missing, [], "main.tf must define every os.environ key activator.py reads")


def _environ_keys(code):
    import ast

    keys = set()
    for node in ast.walk(ast.parse(code)):
        if not isinstance(node, ast.Subscript):
            continue
        value = node.value
        if isinstance(value, ast.Attribute) and value.attr == "environ" and isinstance(node.slice, ast.Constant):
            keys.add(node.slice.value)
    return keys


if __name__ == "__main__":
    print(json.dumps({"suite": "activator"}))
    unittest.main(verbosity=2)
