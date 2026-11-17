"""X-Resource ownership queries fail closed and do not outlive the request."""

import ctypes

from runtime_test_support import *  # noqa: F401,F403
from native.linux.provider import x11_client_identity


class ClientPidWorkerTests(unittest.TestCase):
    def test_returns_only_requested_positive_integer_owners(self):
        result = subprocess.CompletedProcess([], 0, json.dumps({
            "0x1": 71, "0x2": -1, "0x3": 0, "0x4": True,
            "0x5": "72", "0x6": 1.2, "0x7": 99,
        }))
        with mock.patch.object(x11_client_identity.subprocess, "run", return_value=result) as run:
            owners = x11_client_identity.x11_client_pids(
                ["0x1", "0x2", "0x3", "0x4", "0x5", "0x6"])
        self.assertEqual({"0x1": 71}, owners)
        self.assertEqual(3, run.call_args.kwargs["timeout"])
        self.assertEqual([sys.executable, "-I", x11_client_identity.__file__],
                         run.call_args.args[0][:3])

    def test_no_missing_pids_needs_no_worker(self):
        with mock.patch.object(x11_client_identity.subprocess, "run") as run:
            self.assertEqual({}, x11_client_identity.x11_client_pids([]))
        run.assert_not_called()

    def test_unavailable_extension_dead_client_timeout_and_bad_output_are_unidentified(self):
        for result in (
            subprocess.CompletedProcess([], 1, '{"0x1": 71}'),
            subprocess.CompletedProcess([], -11, ""),
            subprocess.CompletedProcess([], 0, "not json"),
            subprocess.CompletedProcess([], 0, "[]"),
            subprocess.CompletedProcess([], 0, "null"),
            subprocess.TimeoutExpired("worker", 3),
            OSError("cannot start worker"),
        ):
            with self.subTest(result=result):
                with mock.patch.object(x11_client_identity.subprocess, "run") as run:
                    if isinstance(result, Exception):
                        run.side_effect = result
                    else:
                        run.return_value = result
                    self.assertEqual({}, x11_client_identity.x11_client_pids(["0x1"]))


class XResourceQueryTests(unittest.TestCase):
    def setUp(self):
        self.xlib, self.xres = mock.Mock(), mock.Mock()
        self.xlib.XOpenDisplay.return_value = 100
        self.version = (1, 2)
        self.pids = [71]
        self.xres.XResQueryVersion.side_effect = self.query_version
        self.xres.XResQueryClientIds.side_effect = self.query_ids
        self.xres.XResGetClientPid.side_effect = lambda value: value._obj.spec.client
        patcher = mock.patch.object(x11_client_identity, "_xres_libraries",
                                    return_value=(self.xlib, self.xres))
        patcher.start()
        self.addCleanup(patcher.stop)

    def query_version(self, display, major, minor):
        major._obj.value, minor._obj.value = self.version
        return 1

    def query_ids(self, display, count, spec, result_count, result_values):
        self.assertEqual(100, display)
        self.assertEqual(1, count)
        self.assertEqual(0x12, spec._obj.client)
        self.assertEqual(2, spec._obj.mask)
        self.values = (x11_client_identity._ClientIdValue * len(self.pids))()
        for index, pid in enumerate(self.pids):
            self.values[index].spec.client = pid
        result_count._obj.value = len(self.values)
        ctypes.cast(result_values, ctypes.POINTER(
            ctypes.POINTER(x11_client_identity._ClientIdValue)))[0] = self.values
        return 0

    def test_query_uses_xid_and_releases_reply_and_display(self):
        owners = x11_client_identity._query_client_pids(["0x12"])
        self.assertEqual({"0x12": 71}, owners)
        self.xres.XResClientIdsDestroy.assert_called_once()
        self.xlib.XCloseDisplay.assert_called_once_with(100)

    def test_no_local_pid_or_multiple_owners_cannot_bind(self):
        for pids in ([], [0], [71, 72]):
            with self.subTest(pids=pids):
                self.pids = pids
                self.assertEqual({}, x11_client_identity._query_client_pids(["0x12"]))
        self.assertEqual(3, self.xres.XResClientIdsDestroy.call_count)
        self.xres.XResGetClientPid.side_effect = lambda value: -1
        self.assertEqual({}, x11_client_identity._query_client_pids(["0x12"]))

    def test_old_extension_does_not_issue_unsupported_request(self):
        self.version = (1, 0)
        self.assertEqual({}, x11_client_identity._query_client_pids(["0x12"]))
        self.xres.XResQueryClientIds.assert_not_called()
        self.xlib.XCloseDisplay.assert_called_once_with(100)

    def test_failed_reply_is_not_reused_or_freed_twice(self):
        self.xres.XResQueryClientIds.side_effect = None
        self.xres.XResQueryClientIds.return_value = 1
        self.assertEqual({}, x11_client_identity._query_client_pids(["0x12"]))
        self.xres.XResGetClientPid.assert_not_called()
        self.xres.XResClientIdsDestroy.assert_not_called()
        self.xlib.XCloseDisplay.assert_called_once_with(100)

    def test_missing_display_or_extension_has_no_owner(self):
        self.xlib.XOpenDisplay.return_value = None
        self.assertEqual({}, x11_client_identity._query_client_pids(["0x12"]))
        self.xlib.XCloseDisplay.assert_not_called()
        self.xlib.XOpenDisplay.return_value = 100
        self.xres.XResQueryVersion.side_effect = None
        self.xres.XResQueryVersion.return_value = 0
        self.assertEqual({}, x11_client_identity._query_client_pids(["0x12"]))
        self.xres.XResQueryClientIds.assert_not_called()
        self.xlib.XCloseDisplay.assert_called_once_with(100)

    def test_root_and_invalid_resource_ids_never_query_all_clients(self):
        self.assertEqual({}, x11_client_identity._query_client_pids(["0x0", "0x100000000"]))
        self.xres.XResQueryClientIds.assert_not_called()
