import logging
import socketserver
import threading

logger = logging.getLogger(__name__)

# Minimal HTTP response (just enough for the liveness probe)
HEALTH_RESPONSE = b"HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"


class HealthCheckHandler(socketserver.BaseRequestHandler):
    def handle(self):
        # We don't actually need to read the request.  Just send the 200 OK.
        self.request.sendall(HEALTH_RESPONSE)


def _run_health_server():
    with socketserver.TCPServer(("0.0.0.0", 7654), HealthCheckHandler) as server:
        server.serve_forever()


def run_health_server():
    # Start the health check server in a separate thread
    health_thread = threading.Thread(target=_run_health_server)  # Or any port
    # TODO is this good?
    health_thread.daemon = (
        True  # Allow the main process to exit even if the thread is running
    )
    health_thread.start()
    logger.info("health server started")
