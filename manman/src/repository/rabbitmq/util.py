from typing import Optional


def add_routing_key_prefix(routing_key: str, prefix: Optional[str]) -> str:
    """
    Add a prefix to the routing key.

    :param routing_key: The original routing key.
    :param prefix: The prefix to add.
    :return: The modified routing key with the prefix added.
    """
    if prefix is None or len(prefix) == 0 or len(routing_key) == 0:
        return routing_key

    if not routing_key.startswith("."):
        routing_key = f"{prefix}.{routing_key}"
    else:
        routing_key = f"{prefix}{routing_key}"

    return routing_key


def add_routing_key_suffix(routing_key: str, suffix: Optional[str]) -> str:
    """
    Add a suffix to the routing key.

    :param routing_key: The original routing key.
    :param suffix: The suffix to add.
    :return: The modified routing key with the suffix added.
    """
    if suffix is None or len(suffix) == 0 or len(routing_key) == 0:
        return routing_key

    if not routing_key.endswith("."):
        routing_key += "."
    return f"{routing_key}{suffix}"
