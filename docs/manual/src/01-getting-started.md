# 1. Getting started

## Requirements

- Linux or macOS, x86-64 or ARM64. One directory on a local filesystem for
  the data root; OpenS3 keeps everything there (chapter 8 shows the layout).
- Go 1.27 or later to build from source. Docker is optional (used for the
  container image and the conformance test suites).

## Build

```sh
git clone https://gitlab.com/Birdsall/opens3.git
cd opens3
make build            # produces bin/opens3
```

Or build the container image: `make docker`, or `docker compose up` with
`OPENS3_ROOT_PASSWORD` set (see `docker-compose.yml`).

## First start

Root credentials are required; the server refuses to start without them.
Choose a root user name and a long random password:

```sh
export OPENS3_ROOT_USER=root
export OPENS3_ROOT_PASSWORD="$(openssl rand -base64 24)"
bin/opens3 server --root /var/lib/opens3 --address :9000
```

On first start the server:

- creates the data root with `meta/` (the metadata database and the master
  key file), `data/` (object bytes) and `tmp/`;
- generates a random **master key** in `meta/master.keys` and logs its
  fingerprint with a warning to back it up. Every stored secret and every
  encrypted object depends on it (chapter 5 and chapter 8);
- creates the built-in policies and the default encryption key;
- listens on port 9000 for the S3 API, the IAM API, the admin API and the
  console.

Check it is up:

```sh
curl -i http://localhost:9000/opens3/health/ready     # 200, empty body
```

Open the console at `http://localhost:9000/console/` and sign in with the
root user name and password. Over plain HTTP from another machine the
console shows a warning banner; chapter 2 covers TLS.

## First bucket with the AWS CLI

Any S3 client works. With the AWS CLI:

```sh
export AWS_ACCESS_KEY_ID=$OPENS3_ROOT_USER
export AWS_SECRET_ACCESS_KEY=$OPENS3_ROOT_PASSWORD
export AWS_DEFAULT_REGION=us-east-1
export AWS_ENDPOINT_URL=http://localhost:9000

aws s3 mb s3://demo
aws s3 cp README.md s3://demo/
aws s3 ls s3://demo/
aws sts get-caller-identity
```

`AWS_ENDPOINT_URL` is honoured by the AWS CLI v2 and recent SDKs; older
tools take `--endpoint-url`. Path-style and virtual-host-style addressing
are both supported (chapter 2, `OPENS3_DOMAINS`).

## First user

Do not use the root credentials in applications. Create a user, a key for
programs and, if the person needs the console, a password:

```sh
aws iam create-user --user-name alice
aws iam attach-user-policy --user-name alice --policy-arn arn:aws:iam::aws:policy/readwrite
aws iam create-access-key --user-name alice          # shows the secret once
aws iam create-login-profile --user-name alice --password 'a long passphrase'
```

The same can be done in the console under Identity. Chapter 3 explains the
model.

## Stopping and restarting

Stop with Ctrl-C or SIGTERM; in-flight requests are given thirty seconds.
Restart with the same `--root` and the same root credentials. Changing the
root password is safe: it is only a credential, nothing is derived from it.

## Where to look next

- Chapter 2 for every setting.
- Chapter 5 before you rely on encryption.
- Chapter 8 before you rely on backups.
