import abc


class MessagePublisherInterface(abc.ABC):
    @abc.abstractmethod
    def publish(self, message: str) -> None:
        pass


class MessageSubscriberInterface(abc.ABC):
    @abc.abstractmethod
    def consume(self) -> list[str]:
        """
        Retrieve a list of messages from the message provider.

        Non-blocking
        """
        # TODO - allow consuming variable amount
        # not needed, yet
        pass
