# Repository Guidelines

## Project Structure & Module Organization
The runtime code lives in `lambda/`, with `main.go` wiring AWS events into the `policyloader` and `policyevaluator` packages. Reference Rego policies (`lambda/policies`), payload samples (`lambda/inputs`), and temp artifacts such as `bootstrap` or `lambda-deployment.zip` stay under the same directory. Infrastructure definitions are isolated in `cloudformation/opa-lambda-stack.yaml`; update that template whenever IAM, S3, or networking resources change.

## Build, Test, and Development Commands
`cd lambda && GOOS=linux GOARCH=amd64 go build -o bootstrap main.go` cross-compiles the Lambda binary. `zip lambda-deployment.zip bootstrap` creates the upload artifact, and `aws lambda update-function-code --zip-file fileb://lambda/lambda-deployment.zip` refreshes code in-place. `go test ./...` executes the suite, while `cat inputs/example-input.json | go run main.go example` lets you validate policies locally. Use the CloudFormation create/update commands from `README.md` to provision or mutate stacks; always wait for `aws cloudformation wait` before pushing further changes.

## Coding Style & Naming Conventions
Run `go fmt ./...` before sending a review; the repo sticks to idiomatic Go formatting (tabs, goimports ordering). Keep package names lowercase and singular, and align Rego package names with their file paths (`policies/auth/user.rego` → `package auth.user`). Environment variables such as `S3_BUCKET` should be upper snake case, and log messages should reuse the `logrus` patterns already in `main.go`.

## Testing Guidelines
Unit tests coexist with implementation files as `_test.go` siblings and commonly rely on `stretchr/testify` (see `lambda/main_test.go`). Favor table-driven tests that cover the three event types (`alb`, `apigw-proxy`, `apigw-v2`). Run `go test ./...` before every commit; if you add integration tests that require AWS, guard them with build tags and document the expected environment variables.

## Commit & Pull Request Guidelines
History shows concise, imperative commit subjects (“Add tests for ALB and API Gateway handlers”); follow that phrasing and keep scope tight. Every PR should explain the motivation, list the build/test commands executed, and link any tracked issue or deployment ticket. Include CLI snippets (e.g., `aws cloudformation create-stack ...`) when proposing runbook updates so reviewers can replay them.

## Deployment & Environment Notes
Never check in credentials or generated zips. After building, upload policies via `aws s3 sync policies/ s3://<bucket>/policies/` to keep package resolution consistent, then run `aws lambda update-function-code ...` or update the CloudFormation stack. Call out any change that affects cold-start caching or IAM permissions so operators can refresh their stacks safely.
