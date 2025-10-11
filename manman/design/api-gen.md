This guide outlines the steps for a coding agent to implement a robust, Python-specific system for generating separate OpenAPI clients that share a unified set of data models, suitable for consumption by third-party applications.

> **📚 IMPLEMENTATION COMPLETE!**
>
> This design has been fully implemented for the ManMan monorepo using Bazel.
>
> ### 🚀 Current Implementation
> - **OpenAPI clients** are generated using `//tools:openapi_client.bzl` Bazel rule
> - **No external tools needed** - Everything runs through Bazel build system
> - **Test the clients**: `bazel test //tools/client_codegen:test_experience_api_client`
>
> ### 📖 Key Files  
> - **[tools/openapi_client.bzl](../../tools/openapi_client.bzl)** - Client generation rule
> - **[tools/client_codegen/BUILD.bazel](../../tools/client_codegen/BUILD.bazel)** - Client target definitions
> - **[OPENAPI_CLIENT_IMPLEMENTATION_SUMMARY.md](../../OPENAPI_CLIENT_IMPLEMENTATION_SUMMARY.md)** - Implementation details
>
> ### ⚡ Quick Start
> ```bash
> # Build all clients
> bazel build //tools/client_codegen:manman_experience_api
> bazel build //tools/client_codegen:manman_status_api
> 
> # Test clients
> bazel test //tools/client_codegen:test_experience_api_client
> ```

-----

## 🚀 Client Generation Handoff Guide

### Goal

To generate independently distributed Python client libraries (e.g., `client-a.whl`, `client-b.whl`) that use the **authoritative Python classes** defined in a centralized `shared/models` package. This eliminates object duplication and ensures the generated clients are **self-contained** for external consumption via tools like `pip`.

-----

### 1\. Prerequisites (Monorepo Setup)

| Item | Requirement |
| :--- | :--- |
| **Shared Models** | Pydantic models (e.g., `User`, `Address`) must be defined in a single, accessible package (e.g., `/shared/models/common.py`). |
| **Microservices** | FastAPI services must import and use these shared models directly in their route definitions to ensure identical OpenAPI schemas. |
| **Tools** | The build environment must have **`openapi-generator-cli`** installed and available. |

-----

### 2\. Implementation Steps (Build Script)

A Python build script (e.g., `build_scripts/generate_clients.py`) must be implemented to manage the configuration, generation, and packaging for each client.

#### Step 2.1: Dynamic Configuration Generation

The script must programmatically inspect the shared models (`shared.models.common`) and generate a temporary JSON configuration file for the OpenAPI Generator for each client.

| Config Key | Value | Purpose |
| :--- | :--- | :--- |
| `"importMappings"` | `{"User": "shared.models.common.User"}` | Tells the generator what import path to write. |
| `"typeMappings"` | `{"User": "User"}` | Prevents the generator from creating a redundant `User` class definition. |
| `"packageName"` | E.g., `"client_a"` | Sets the package name for distribution. |

#### Step 2.2: File Inclusion (Self-Containment)

After the client code is generated into its output directory (e.g., `clients/client_a`), the script **must copy** the source code of the `shared/models` package into the client's source tree.

  * **Source:** `/shared/models/`
  * **Destination:** `/clients/<client_name>/shared/models/`

This ensures that the `import shared.models.common` statement within the generated client resolves correctly when the package is installed externally.

#### Step 2.3: Execute Codegen and Packaging

The script must execute the `openapi-generator-cli` and then package the client.

1.  **Codegen Command:** Run the CLI using the temporary config file (`-c`) for each service specification (`-i`).

    ```bash
    openapi-generator generate -i <spec_url> -g python -c <tmp_config.json> -o clients/<client_name>
    ```

2.  **Packaging:** For each generated client, execute the Python packaging command (e.g., `python setup.py sdist bdist_wheel` or using modern tooling) to create a distributable artifact that includes the copied `shared` directory.

-----

### 3\. Verification

The coding agent should confirm the following after generation:

1.  The generated client source code contains the expected import: `from shared.models.common import User`.
2.  The generated client source **does not** contain a redundant definition of the `User` class.
3.  The final distributable package (`.whl` or `.tar.gz`) contains the copied source files under a path that resolves the import (e.g., `client_a/shared/models/common.py`).