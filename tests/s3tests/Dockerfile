# Runner image for the Ceph s3-tests suite. Built by run.sh; the checkout
# itself is bind-mounted at /s3-tests so the image only carries dependencies.
FROM python:3.11-slim
RUN apt-get update && apt-get install -y --no-install-recommends git && rm -rf /var/lib/apt/lists/*
COPY requirements.txt /tmp/requirements.txt
RUN pip install --no-cache-dir -r /tmp/requirements.txt pytest-timeout
WORKDIR /s3-tests
