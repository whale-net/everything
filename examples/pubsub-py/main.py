import time
import threading
import queue

class PubSub:
    def __init__(self):
        self.subscribers = []
        self.message_queue = queue.Queue()

    def subscribe(self, subscriber_func):
        self.subscribers.append(subscriber_func)

    def publish(self, message):
        print(f"Publishing message: {message}")
        self.message_queue.put(message)

    def _process_messages(self):
        while True:
            message = self.message_queue.get()
            for subscriber in self.subscribers:
                subscriber(message)
            self.message_queue.task_done()

    def start(self):
        threading.Thread(target=self._process_messages, daemon=True).start()

def subscriber_one(message):
    print(f"Subscriber One received: {message}")

def subscriber_two(message):
    print(f"Subscriber Two received: {message}")

if __name__ == "__main__":
    pubsub = PubSub()
    pubsub.subscribe(subscriber_one)
    pubsub.subscribe(subscriber_two)
    pubsub.start()

    pubsub.publish("Hello, Pub/Sub!")
    time.sleep(1)
    pubsub.publish("Another message!")
    time.sleep(2)
    print("Pub/Sub example finished.")