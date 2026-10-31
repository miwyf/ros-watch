import logging
import signal
import threading

from waitress import create_server

from .collector import Collector
from .config import Config
from .storage import Store
from .web import create_app


def main():
    logging.basicConfig(
        level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s"
    )
    config = Config()
    config.validate()
    store = Store(config)
    collector = Collector(config, store)
    app = create_app(config, store, collector)
    if config.demo:
        from .demo import seed

        seed(store)
    collector.start()
    server = create_server(
        app,
        host=config.http_host,
        port=config.http_port,
        threads=4,
        connection_limit=64,
        channel_timeout=30,
        max_request_body_size=4096,
        clear_untrusted_proxy_headers=True,
        ident="ROS-Watch",
    )

    def stop(*_):
        collector.stop()
        server.close()
        server.task_dispatcher.shutdown()

    for sig in (signal.SIGINT, signal.SIGTERM):
        signal.signal(
            sig, lambda *args: threading.Thread(target=stop, daemon=True).start()
        )
    logging.info(
        "ROS Watch started (HTTP %s, UDP %s)", config.http_port, config.syslog_port
    )
    try:
        server.run()
    finally:
        collector.stop()


if __name__ == "__main__":
    main()
