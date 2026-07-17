import importlib.util
import os
import sys
import types
import unittest
from pathlib import Path


class FakePaginator:
    def __init__(self):
        self.kwargs = None

    def paginate(self, **kwargs):
        self.kwargs = kwargs
        return [{"taskArns": []}]


class FakeECS:
    def __init__(self):
        self.paginator = FakePaginator()
        self.run_kwargs = None
        self.describe_calls = []

    def get_paginator(self, name):
        if name != "list_tasks":
            raise AssertionError(f"unexpected paginator {name}")
        return self.paginator

    def run_task(self, **kwargs):
        self.run_kwargs = kwargs
        return {"tasks": []}

    def describe_tasks(self, **kwargs):
        self.describe_calls.append(kwargs)
        return {"tasks": [{"taskArn": arn} for arn in kwargs["tasks"]]}


class FakeTable:
    table_name = "state"

    def __init__(self, scan_pages=None):
        self.meta = types.SimpleNamespace(client=types.SimpleNamespace())
        self.scan_pages = list(scan_pages or [])
        self.scan_calls = []

    def scan(self, **kwargs):
        self.scan_calls.append(kwargs)
        if not self.scan_pages:
            return {}
        return self.scan_pages.pop(0)


class FakeBoto3(types.ModuleType):
    def __init__(self, ecs, table):
        super().__init__("boto3")
        self.ecs = ecs
        self.table = table

    def client(self, name):
        if name != "ecs":
            raise AssertionError(f"unexpected client {name}")
        return self.ecs

    def resource(self, name):
        if name != "dynamodb":
            raise AssertionError(f"unexpected resource {name}")
        return types.SimpleNamespace(Table=lambda _: self.table, meta=self.table.meta)


def load_activator(fake_ecs, fake_table=None):
    fake_boto3 = FakeBoto3(fake_ecs, fake_table or FakeTable())
    fake_botocore = types.ModuleType("botocore")
    fake_botocore_exceptions = types.ModuleType("botocore.exceptions")
    fake_botocore_exceptions.ClientError = RuntimeError
    fake_botocore.exceptions = fake_botocore_exceptions
    fake_urllib3 = types.ModuleType("urllib3")
    fake_urllib3.PoolManager = lambda **_: types.SimpleNamespace()
    fake_urllib3.Timeout = lambda **_: None
    fake_urllib3.exceptions = types.SimpleNamespace(HTTPError=RuntimeError)
    sys.modules.update({
        "boto3": fake_boto3,
        "botocore": fake_botocore,
        "botocore.exceptions": fake_botocore_exceptions,
        "urllib3": fake_urllib3,
    })
    values = {
        "CLUSTER": "qualification-cluster",
        "TASK_DEFINITION": "arn:aws:ecs:region:account:task-definition/qualification:1",
        "STARTED_BY": "qualification-scale-zero",
        "SUBNETS": "subnet-a",
        "SECURITY_GROUPS": "sg-a",
        "PORT": "8080",
        "REPLICAS": "2",
        "STARTUP_TIMEOUT": "20",
        "REQUEST_TIMEOUT": "25",
        "STATE_TABLE": "qualification-state",
    }
    old = os.environ.copy()
    os.environ.update(values)
    try:
        path = Path(__file__).parents[2] / "deploy" / "ecs-scale-zero" / "activator.py"
        spec = importlib.util.spec_from_file_location("qualification_activator", path)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        return module
    finally:
        os.environ.clear()
        os.environ.update(old)


class ActivatorQualification(unittest.TestCase):
    def test_gateway_event_requires_explicit_routing_fields(self):
        ecs = FakeECS()
        activator = load_activator(ecs)
        valid = {
            "requestContext": {"requestId": "request-1", "http": {"method": "POST"}},
            "rawPath": "/api/test",
            "rawQueryString": "",
            "headers": {"content-type": "application/json"},
            "body": "{}",
            "isBase64Encoded": False,
        }
        request = activator.parse_gateway_request(valid)
        self.assertEqual(request.method, "POST")
        self.assertEqual(request.raw_path, "/api/test")
        for field in (("requestContext",), ("requestContext", "http"), ("requestContext", "requestId"), ("rawPath",)):
            invalid = dict(valid)
            if len(field) == 1:
                invalid.pop(field[0])
            else:
                invalid[field[0]] = dict(invalid[field[0]])
                invalid[field[0]].pop(field[1])
            with self.assertRaises(activator.ActivationError):
                activator.parse_gateway_request(invalid)

    def test_started_by_is_the_only_list_tasks_filter(self):
        ecs = FakeECS()
        activator = load_activator(ecs)
        self.assertEqual(activator.running_tasks(), [])
        self.assertEqual(
            ecs.paginator.kwargs,
            {"cluster": "qualification-cluster", "startedBy": "qualification-scale-zero"},
        )

    def test_run_task_uses_the_same_started_by_scope(self):
        ecs = FakeECS()
        activator = load_activator(ecs)
        activator.start_tasks()
        self.assertEqual(ecs.run_kwargs["startedBy"], "qualification-scale-zero")
        self.assertEqual(ecs.run_kwargs["count"], 2)

    def test_running_tasks_describes_bounded_streamed_batches(self):
        ecs = FakeECS()
        ecs.paginator.paginate = lambda **_: [
            {"taskArns": [f"task-{index}" for index in range(75)]},
            {"taskArns": [f"task-{index}" for index in range(75, 125)]},
        ]
        activator = load_activator(ecs)
        tasks = activator.running_tasks()
        self.assertEqual([len(call["tasks"]) for call in ecs.describe_calls], [100, 25])
        self.assertEqual(len(tasks), 125)

    def test_active_lease_scan_exits_without_materializing_pages(self):
        ecs = FakeECS()
        table = FakeTable([
            {"Items": [{"id": "lease:active"}], "LastEvaluatedKey": {"id": "page-1"}},
            {"Items": [{"id": "lease:later"}]},
        ])
        activator = load_activator(ecs, table)
        self.assertTrue(activator.has_active_lease())
        self.assertEqual(len(table.scan_calls), 1)


if __name__ == "__main__":
    unittest.main()
