# Lambda Function for Open Policy Agent

## Overview

This Lambda function evaluates JSON payloads against [Open Policy Agent's (OPA)](https://openpolicyagent.org/) [Rego policies](https://www.openpolicyagent.org/docs/latest/policy-language/). It can be invoked directly (synchronous `Invoke` API), or fronted by Application Load Balancers and both versions of API Gateway (REST v1 and HTTP v2), returning policy decisions in milliseconds. Policies can be retrieved from multiple backends (S3, the local filesystem for development, or an HTTP policy service) and are cached in-memory—and optionally on disk—to minimize latency and backend traffic.

## Policy Backends

Regardless of the backend, policy names map to package names inside `.rego` files (`auth.user` → `package auth.user`). The loaders normalize dotted names into directory paths (`auth/user.rego`). The mapping rules are:

- File `policies/example.rego` containing `package example` is addressed as policy `example`.
- File `policies/auth/user.rego` containing `package auth.user` is addressed as policy `auth.user`.
- Internally the loader prepends `policies.`, converts dots to `/`, and appends `.rego`, so `auth.user` becomes `policies/auth/user.rego`.

Keeping the folder structure and package names identical across local development, S3, and policy service deployments avoids unexpected cache misses or eval errors.

### S3 Buckets

Set the `S3_BUCKET` environment variable to point at the bucket that stores your policies. Organize files under a `policies/` prefix so the loader can find `policies/<path>.rego` objects. A typical bucket might look like this:

```
s3://opa-policies-dev/
└── policies/
    ├── example.rego          # package example
    ├── auth/
    │   └── user.rego        # package auth.user
    └── teams/
        └── ownership.rego   # package teams.ownership
```

Use `aws s3 sync policies/ s3://<bucket>/policies/` during deployment to keep the bucket current. Versioning the bucket helps you recover from accidental policy pushes.

### Local Filesystem (Development)

When `S3_BUCKET` and the policy service variables are unset, the Lambda (or `go run`) reads policies directly from the local `lambda/policies/` directory. Mirror the same layout you keep in S3 so that policy names behave identically across environments:

```
lambda/policies/
├── example.rego
├── auth/
│   └── user.rego
└── teams/
    └── ownership.rego
```

Local evaluation samples live under `lambda/inputs/`, making it easy to iterate on Rego files with `cat inputs/example-input.json | go run main.go auth.user`.

### HTTP Policy Service

To decouple policy distribution from S3, set `POLICY_SERVICE_URL` to an HTTPS endpoint that serves `.rego` files. The Lambda issues authenticated `GET` requests for individual modules and respects HTTP caching headers.

- **Request shape** – The loader calls `GET {POLICY_SERVICE_URL}/{POLICY_RESOURCE_PREFIX?}/{policy-path}.rego`. For example, when evaluating policy `auth.user` the loader requests `/policies/auth/user.rego` (assuming `POLICY_RESOURCE_PREFIX=policies`). Policy `teams.ownership` becomes `/policies/teams/ownership.rego`. If you omit the prefix the request path is simply `/auth/user.rego`.
- **Authentication** – Provide `POLICY_BEARER_TOKEN` to send `Authorization: Bearer <token>` on every request. Any bearer-compatible auth mechanism works (API Gateway usage plans, OAuth2 service tokens, etc.).
- **Caching** – The loader caches each policy in memory and stores the last `ETag`. It sends `If-None-Match: <etag>` on every refresh and expects `304 Not Modified` when the file is unchanged. When `POLICY_PERSIST=true` (default), downloaded files are written to `/tmp/.opa/policies` or a custom `POLICY_CACHE_DIR` so they survive cold starts. Example response headers:
  - `200 OK` with `Etag: "sha256-<digest>"` and the policy body when the file changed
  - `304 Not Modified` with the same `Etag` value when serving from cache
- **Error handling** – Return `404` if a policy is missing. Other `4xx/5xx` responses cause the loader to log the failure and continue serving the previous cached copy.

Keep the service’s storage layout identical to S3/local (for example, `/policies/auth/user.rego` on disk or in an object store) so the request path translates directly to the underlying file. The service can stream files from a database, another bucket, or even generate them on the fly as long as the final response body matches the `.rego` module referenced by the policy name.

Use these environment variables to tune the integration:

| Variable | Description |
| --- | --- |
| `POLICY_SERVICE_URL` | Base URL of the service (required to enable the backend). |
| `POLICY_RESOURCE_PREFIX` | Prepended prefix such as `policies` (optional). |
| `POLICY_BEARER_TOKEN` | Optional bearer token sent via `Authorization` header. |
| `POLICY_PERSIST` | `true/false` (default `true`); control on-disk caching under `/tmp`. |
| `POLICY_POLL_MIN_SECONDS` / `POLICY_POLL_MAX_SECONDS` | Min/max interval between revalidation requests (defaults 10s / 30s). |
| `POLICY_HTTP_TIMEOUT_SECONDS` | HTTP client timeout (default 15s). |
| `POLICY_CACHE_DIR` | Custom cache directory when running locally. |

This contract is intentionally minimal so you can implement the service behind API Gateway, ALB, or any HTTPS platform. Returning deterministic `ETag` values (for example, a SHA256 hash of the file) ensures cache hits across concurrent Lambda invocations.

## Repository Layout

```
.
├── AGENTS.md                       # Additional repo guidance
├── cloudformation/
│   └── opa-lambda-stack.yaml       # CloudFormation template
├── lambda/
│   ├── inputs/                     # Sample ALB/API Gateway payloads
│   ├── policies/                   # Reference Rego policies
│   ├── policyevaluator/            # OPA evaluation helpers
│   ├── policyloader/               # S3/filesystem/policy-service loaders
│   ├── main.go                     # Lambda entry point
│   └── main_test.go
├── LICENSE
└── README.md
```

## Deployment

### Prerequisites

Before deploying, ensure you have:

- AWS CLI configured with credentials
- Go 1.21 or later (for building/testing the function)
- An AWS account with permissions to create Lambda functions, S3 buckets, and IAM roles

### Build the Lambda Artifact

All deployment paths require a Linux binary packaged into `lambda-deployment.zip`:

```sh
cd lambda
GOOS=linux GOARCH=amd64 go build -o bootstrap main.go
zip lambda-deployment.zip bootstrap
```

### Option 1: AWS CloudFormation (Infrastructure as Code)

A templated stack lives in `cloudformation/opa-lambda-stack.yaml`.

**Create or update the stack:**
```sh
aws cloudformation create-stack \
  --stack-name opa-lambda-dev \
  --template-body file://cloudformation/opa-lambda-stack.yaml \
  --parameters \
    ParameterKey=Environment,ParameterValue=dev \
    ParameterKey=FunctionName,ParameterValue=opa-lambda \
    ParameterKey=EnableXRayTracing,ParameterValue=false \
  --capabilities CAPABILITY_NAMED_IAM

aws cloudformation wait stack-create-complete --stack-name opa-lambda-dev
```

**Upload policy files:**
```sh
BUCKET_NAME=$(aws cloudformation describe-stacks \
  --stack-name opa-lambda-dev \
  --query 'Stacks[0].Outputs[?OutputKey==`PolicyBucketName`].OutputValue' \
  --output text)

cd lambda
aws s3 sync policies/ s3://${BUCKET_NAME}/policies/
```

**Update or delete the stack:**
```sh
aws cloudformation update-stack \
  --stack-name opa-lambda-dev \
  --template-body file://cloudformation/opa-lambda-stack.yaml \
  --parameters ParameterKey=Environment,ParameterValue=dev ParameterKey=FunctionName,ParameterValue=opa-lambda \
  --capabilities CAPABILITY_NAMED_IAM

# When tearing down the stack
aws s3 rm s3://${BUCKET_NAME} --recursive
aws cloudformation delete-stack --stack-name opa-lambda-dev
```

### Option 2: AWS Console (Manual Deployment)

1. **Create S3 Bucket** – S3 console → **Create bucket** (e.g., `opa-policies-dev`). Keep public access blocked.
2. **Upload Policies** – Upload `.rego` files under `policies/` (e.g., `policies/example.rego`, `policies/auth/user.rego`).
3. **Create IAM Role** – IAM console → **Roles** → **Create role** → Service **Lambda**. Attach `AWSLambdaBasicExecutionRole` and add an inline policy granting `s3:GetObject`/`s3:ListBucket` to your bucket.
4. **Create Lambda Function** – Lambda console → **Create function** (Author from scratch). Runtime **Amazon Linux 2023**, architecture **x86_64**, execution role `opa-lambda-execution-role`.
5. **Upload Code & Configure** – Upload `lambda-deployment.zip`, set memory (512 MB), timeout (30s), and environment variable `S3_BUCKET=opa-policies-dev`. Add optional policy-service variables as needed.
6. **Test** – Use the Lambda console test event:
   ```json
   {
     "policy": "example",
     "payload": {
       "membership": {
         "user": {
           "login": "testuser",
           "mail": "test@example.com"
         }
       }
     }
   }
   ```
7. **Optional** – Attach VPC networking (`AWSLambdaVPCAccessExecutionRole`) or enable X-Ray tracing (`AWSXRayDaemonWriteAccess`).

### Option 3: AWS CLI (Scripted Manual Deployment)

```sh
# Create bucket and upload policies
aws s3 mb s3://opa-policies-dev --region us-east-1
cd lambda && aws s3 sync policies/ s3://opa-policies-dev/policies/

# Create IAM role
cat > /tmp/trust-policy.json <<'POLICY'
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Principal": {"Service": "lambda.amazonaws.com"},
    "Action": "sts:AssumeRole"
  }]
}
POLICY
aws iam create-role --role-name opa-lambda-execution-role --assume-role-policy-document file:///tmp/trust-policy.json
aws iam attach-role-policy --role-name opa-lambda-execution-role --policy-arn arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole

cat > /tmp/s3-policy.json <<'POLICY'
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Action": ["s3:GetObject", "s3:ListBucket"],
    "Resource": [
      "arn:aws:s3:::opa-policies-dev",
      "arn:aws:s3:::opa-policies-dev/*"
    ]
  }]
}
POLICY
aws iam put-role-policy --role-name opa-lambda-execution-role --policy-name S3PolicyAccess --policy-document file:///tmp/s3-policy.json
sleep 10

# Deploy the Lambda function
ACCOUNT_ID=$(aws sts get-caller-identity --query Account --output text)
aws lambda create-function \
  --function-name opa-lambda-dev \
  --runtime provided.al2023 \
  --role arn:aws:iam::${ACCOUNT_ID}:role/opa-lambda-execution-role \
  --handler bootstrap \
  --zip-file fileb://lambda-deployment.zip \
  --environment Variables={S3_BUCKET=opa-policies-dev} \
  --timeout 30 \
  --memory-size 512

# Smoke test
aws lambda invoke \
  --function-name opa-lambda-dev \
  --payload '{"policy":"example","payload":{"membership":{"user":{"login":"test","mail":"test@example.com"}}}}' \
  response.json
cat response.json
```

## Invoking the Function

The handler expects a payload with the policy name and arbitrary JSON input:

```json
{
  "policy": "example",
  "payload": {
    "membership": {
      "user": {
        "login": "jane",
        "mail": "jane@example.com"
      }
    }
  }
}
```

The `policy` field must match the package name inside the `.rego` module. For a flat policy such as `policies/example.rego` with `package example`, set `"policy": "example"` (as shown above). Nested packages use dotted notation, so a file `policies/auth/user/regression.rego` with `package auth.user.regression` should be invoked with `"policy": "auth.user.regression"`:

```json
{
  "policy": "auth.user.regression",
  "payload": {
    "membership": {
      "user": {
        "login": "jane",
        "mail": "jane@example.com"
      }
    }
  }
}
```

The loader translates `auth.user.regression` into the path `policies/auth/user/regression.rego`, whether the backend is local disk, S3, or the HTTP policy service. Keeping the naming consistent ensures the same payload works across every environment.

Sample ALB and API Gateway events live under `lambda/inputs/` (`alb-event.json`, `apigw-proxy-event.json`, `apigw-v2-event.json`). Invoke the Lambda directly with those files to emulate each integration:

```sh
aws lambda invoke \
  --function-name opa-lambda-dev \
  --cli-binary-format raw-in-base64-out \
  --payload fileb://lambda/inputs/apigw-v2-event.json \
  response.json
```

Set `isBase64Encoded=true` and base64-encode the body when your integration encodes payloads.

## Local Development

### Run Policies Locally

```sh
cd lambda
cat inputs/example-input.json | go run main.go <policy_name>
```

### Test Against S3 Locally

```sh
cd lambda
S3_BUCKET=my-policy-bucket cat inputs/example-input.json | go run main.go <policy_name>
```

Configure AWS credentials via environment variables or `~/.aws/credentials`.

### Build & Test

```sh
cd lambda
go build -o opa_lambda main.go

go test ./...
```

### Policy Service from Local CLI

```sh
cd lambda
POLICY_SERVICE_URL=https://opa.example.com/service/v1 \
POLICY_RESOURCE_PREFIX=policies \
POLICY_BEARER_TOKEN=$(aws secretsmanager get-secret-value --secret-id opa-policy-token --query SecretString --output text) \
cat inputs/example-input.json | go run main.go example
```

If the service replies with `304 Not Modified`, the loader serves the cached policy transparently.

## Contributing

Issues and pull requests are welcome. Please describe the motivation, commands you ran (`go test ./...`, deployment steps, etc.), and link any related issue or ticket.

## License & Credits

- **License:** [MIT](LICENSE)
- **Credits:** This fork is based on [proactiveops/opa_lambda](https://github.com/proactiveops/opa_lambda) by Proactive Ops and adds deployment automation plus documentation updates.
