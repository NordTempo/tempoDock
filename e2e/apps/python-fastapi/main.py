# FastAPI app built by Railpack (no Dockerfile).
from fastapi import FastAPI

app = FastAPI()


@app.get("/")
def root():
    return {"app": "python-fastapi", "version": "v1"}


@app.get("/healthz")
def healthz():
    return "ok"
