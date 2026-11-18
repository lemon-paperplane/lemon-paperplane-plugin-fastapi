import uvicorn
from fastapi import FastAPI
from config.settings import PORT

app: FastAPI = FastAPI()


def main():
    uvicorn.run("main:app", host="0.0.0.0", port=PORT)


if __name__ == "__main__":
    main()
