"""Thin wrapper around esptool that delegates to the installed package."""

import sys
import esptool

if __name__ == "__main__":
    sys.exit(esptool.main())
