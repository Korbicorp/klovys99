FROM python:3.12-slim

ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 PIP_DISABLE_PIP_VERSION_CHECK=1
RUN apt-get update && apt-get install -y --no-install-recommends tesseract-ocr tesseract-ocr-eng tesseract-ocr-fra libgl1 && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY server.py ./
RUN useradd --create-home --uid 10002 presidio
USER presidio
EXPOSE 8092
CMD ["gunicorn", "--bind", "0.0.0.0:8092", "--workers", "2", "--timeout", "120", "server:app"]
