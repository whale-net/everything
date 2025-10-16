"""Abstract interfaces for message publishers and subscribers."""

import abc
from typing import List


class MessagePublisherInterface(abc.ABC):
    """Abstract interface for message publishers."""
    
    @abc.abstractmethod
    def publish(self, message: str) -> None:
        """
        Publish a message.
        
        Args:
            message: Message content to publish
        """
        pass
    
    @abc.abstractmethod
    def shutdown(self) -> None:
        """Shutdown the publisher and cleanup resources."""
        pass


class MessageSubscriberInterface(abc.ABC):
    """Abstract interface for message subscribers."""
    
    @abc.abstractmethod
    def consume(self) -> List[str]:
        """
        Retrieve a list of messages from the message provider.

        Non-blocking operation that returns available messages.
        
        Returns:
            List of message bodies as strings
        """
        pass
    
    @abc.abstractmethod
    def shutdown(self) -> None:
        """Shutdown the subscriber and cleanup resources."""
        pass
