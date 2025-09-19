from fastapi import FastAPI

from .experience import router as experience_router
from .status import router as status_router
from .worker_dal import server_router, worker_router

# TODO - create second fastapp for internal/external if desired
# all code is going to be in deployable anyway
fastapp = FastAPI()
fastapp.include_router(experience_router)
fastapp.include_router(status_router)

# Worker DAL APIs (typically used by worker services)
worker_dal_app = FastAPI(
    title="ManMan Worker DAL API",
    root_path="/workerdal",
)
worker_dal_app.include_router(server_router)
worker_dal_app.include_router(worker_router)
