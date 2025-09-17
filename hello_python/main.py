"""Hello Python application."""

from libs.python.utils import format_greeting, get_version

def get_message():
    """Get a greeting message."""
    return format_greeting("world from uv and Bazel test")

def main():
    """Main entry point."""
    print(get_message())
    print(f"Version: {get_version()}")
    print("that last commit was too early")

if __name__ == "__main__":
    main()
