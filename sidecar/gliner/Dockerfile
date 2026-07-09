FROM python:3.12-slim

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1

WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY server.py install_model.py ./
RUN useradd --create-home --uid 10001 gliner
USER gliner

EXPOSE 8091
CMD ["python", "/app/server.py"]

