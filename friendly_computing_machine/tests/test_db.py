# TODO - figure out mocking slack bot so I can unblock testing
# import pytest
# from unittest.mock import patch, MagicMock
#
# from friendly_computing_machine.db.db import SessionManager
#
#
# class TestSessionManager:
#     @patch("friendly_computing_machine.db.db.Session")
#     def test_create_session(self, session):
#         sm = SessionManager()
#         assert session.called == 1
#         assert sm.should_close == True
#
#     def test_create_session_no_close(self):
#         pass
#
#     def test_enter(self):
#         pass
#
#     def test_exit_normal(self):
#         pass
#
#     def test_exit_error(self):
#         pass
#
#     def test_exit_no_close(self):
#         pass
#
#     def test_full(self):
#         pass
