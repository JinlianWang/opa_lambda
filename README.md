# Lambda Function for Open Policy Agent

This Lambda function evaluates JSON objects against [Open Policy Agent's (OPA)](https://openpolicyagent.org/) [Rego policy files](https://www.openpolicyagent.org/docs/latest/policy-language/). The application uses a serverless architecture. The Rego files are stored in an S3 Bucket and cached in memory to improve performance.

The function is designed for high throughput, low latency policy evaluation.

## Credits

This project is a fork of [proactiveops/opa_lambda](https://github.com/proactiveops/opa_lambda). The original project was created by Proactive Ops and provided the foundational implementation. This fork includes modifications and enhancements, and AWS CloudFormation deployment and improved documentation.

## Prerequisites

Before deploying, ensure you have:
- AWS CLI configured with appropriate credentials
- Go 1.21 or later installed (for building the function)
- An AWS account with permissions to create Lambda functions, S3 buckets, and IAM roles

## Building the Lambda Function

All deployment options require building the Lambda function first:

```sh
cd lambda
GOOS=linux GOARCH=amd64 go build -o bootstrap main.go
zip lambda-deployment.zip bootstrap
```

This creates `lambda-deployment.zip` which contains the Lambda function code.

## Deployment Options

### Option 1: AWS CloudFormation (Infrastructure as Code)

CloudFormation template is provided in `cloudformation/opa-lambda-stack.yaml`.

**Deploy the stack:**
```sh
aws cloudformation create-stack \
  --stack-name opa-lambda-dev \
  --template-body file://cloudformation/opa-lambda-stack.yaml \
  --parameters \
    ParameterKey=Environment,ParameterValue=dev \
    ParameterKey=FunctionName,ParameterValue=opa-lambda \
    ParameterKey=EnableXRayTracing,ParameterValue=false \
  --capabilities CAPABILITY_NAMED_IAM

# Wait for stack creation
aws cloudformation wait stack-create-complete --stack-name opa-lambda-dev
```

**Update Lambda function code:**
```sh
aws lambda update-function-code \
  --function-name opa-lambda-dev \
  --zip-file fileb://lambda/lambda-deployment.zip
```

**Upload policy files to S3:**
```sh
# Get bucket name from stack outputs
BUCKET_NAME=$(aws cloudformation describe-stacks \
  --stack-name opa-lambda-dev \
  --query 'Stacks[0].Outputs[?OutputKey==`PolicyBucketName`].OutputValue' \
  --output text)

# Upload policies
cd lambda
aws s3 sync policies/ s3://${BUCKET_NAME}/policies/
```

**Update the stack:**
```sh
aws cloudformation update-stack \
  --stack-name opa-lambda-dev \
  --template-body file://cloudformation/opa-lambda-stack.yaml \
  --parameters \
    ParameterKey=Environment,ParameterValue=dev \
    ParameterKey=FunctionName,ParameterValue=opa-lambda \
  --capabilities CAPABILITY_NAMED_IAM
```

**Delete the stack:**
```sh
# Empty S3 bucket first
BUCKET_NAME=$(aws cloudformation describe-stacks \
  --stack-name opa-lambda-dev \
  --query 'Stacks[0].Outputs[?OutputKey==`PolicyBucketName`].OutputValue' \
  --output text)
aws s3 rm s3://${BUCKET_NAME} --recursive

# Delete stack
aws cloudformation delete-stack --stack-name opa-lambda-dev
```

### Option 2: AWS Console (Manual Deployment)

#### Step 1: Create S3 Bucket

1. Navigate to **S3** service in AWS Console
2. Click **Create bucket**
3. Enter bucket name (e.g., `opa-policies-dev`)
4. Choose your AWS Region
5. Keep default settings (Block all public access: ON)
6. Click **Create bucket**

#### Step 2: Upload Policy Files

1. Open your S3 bucket
2. Click **Upload**
3. Upload `.rego` files maintaining the directory structure:
   - Policy `example` → Upload as `policies/example.rego`
   - Policy `auth.user` → Upload as `policies/auth/user.rego`

#### Step 3: Create IAM Role

1. Navigate to **IAM** > **Roles** > **Create role**
2. Select **AWS service** > **Lambda** > **Next**
3. Attach policies:
   - `AWSLambdaBasicExecutionRole` (for CloudWatch logs)
4. Click **Next**, name the role `opa-lambda-execution-role`
5. Click **Create role**
6. Open the role and add an inline policy for S3 access:
   ```json
   {
     "Version": "2012-10-17",
     "Statement": [
       {
         "Effect": "Allow",
         "Action": ["s3:GetObject", "s3:ListBucket"],
         "Resource": [
           "arn:aws:s3:::opa-policies-dev",
           "arn:aws:s3:::opa-policies-dev/*"
         ]
       }
     ]
   }
   ```

#### Step 4: Create Lambda Function

1. Navigate to **Lambda** > **Create function**
2. Choose **Author from scratch**
3. Configure:
   - Function name: `opa-lambda-dev`
   - Runtime: **Amazon Linux 2023**
   - Architecture: **x86_64**
   - Execution role: **Use an existing role** > Select `opa-lambda-execution-role`
4. Click **Create function**

#### Step 5: Configure Lambda

1. **Upload code**: **Code** tab > **Upload from** > **.zip file** > Upload `lambda-deployment.zip`
2. **Configure settings**: **Configuration** > **General configuration**:
   - Memory: 512 MB
   - Timeout: 30 seconds
3. **Set environment variables**: **Configuration** > **Environment variables**:
   - Key: `S3_BUCKET`, Value: `opa-policies-dev`

#### Step 6: Test

1. **Test** tab > **Create new event**
2. Event JSON:
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
3. Click **Test** and verify results

**Optional configurations:**
- **VPC**: **Configuration** > **VPC** (requires `AWSLambdaVPCAccessExecutionRole`)
- **X-Ray**: **Configuration** > **Monitoring tools** > Enable **Active tracing** (requires `AWSXRayDaemonWriteAccess`)

### Option 3: AWS CLI (Scripted Manual Deployment)

```sh
# Create S3 bucket
aws s3 mb s3://opa-policies-dev --region us-east-1

# Upload policies
cd lambda
aws s3 sync policies/ s3://opa-policies-dev/policies/

# Create IAM role (create trust-policy.json first)
cat > /tmp/trust-policy.json <<EOF
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Principal": {"Service": "lambda.amazonaws.com"},
    "Action": "sts:AssumeRole"
  }]
}
EOF

aws iam create-role \
  --role-name opa-lambda-execution-role \
  --assume-role-policy-document file:///tmp/trust-policy.json

# Attach managed policy
aws iam attach-role-policy \
  --role-name opa-lambda-execution-role \
  --policy-arn arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole

# Create and attach S3 policy
cat > /tmp/s3-policy.json <<EOF
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
EOF

aws iam put-role-policy \
  --role-name opa-lambda-execution-role \
  --policy-name S3PolicyAccess \
  --policy-document file:///tmp/s3-policy.json

# Wait for IAM role to propagate
sleep 10

# Create Lambda function
aws lambda create-function \
  --function-name opa-lambda-dev \
  --runtime provided.al2023 \
  --role arn:aws:iam::$(aws sts get-caller-identity --query Account --output text):role/opa-lambda-execution-role \
  --handler bootstrap \
  --zip-file fileb://lambda-deployment.zip \
  --environment Variables={S3_BUCKET=opa-policies-dev} \
  --timeout 30 \
  --memory-size 512

# Test the function
aws lambda invoke \
  --function-name opa-lambda-dev \
  --payload '{"policy":"example","payload":{"membership":{"user":{"login":"test","mail":"test@example.com"}}}}' \
  response.json

cat response.json
```

## Invoking the Function

Once deployed, invoke the Lambda function with this payload structure (see `lambda/inputs/lambda-event.json` for the example values):

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

- `policy`: The policy name (e.g., `example`, `auth.user`)
- `payload`: The input data to evaluate against the policy

## Local Development

### Policy Naming Convention

The policy name must match the package name in your `.rego` file:
- File: `policies/example.rego` with `package example` → Policy name: `example`
- File: `policies/auth/user.rego` with `package auth.user` → Policy name: `auth.user`

The policy loader automatically:
1. Prepends `policies.` to the policy name
2. Converts dots (`.`) to directory separators (`/`)
3. Appends `.rego` extension

Example: Policy name `auth.user` → File path `policies/auth/user.rego`

### Running Locally

```sh
cd lambda
cat inputs/example-input.json | go run main.go <policy_name>
```

Example test data files are provided in `lambda/inputs/`.

### S3 Integration Testing

Test with S3 by setting the `S3_BUCKET` environment variable:

```sh
cd lambda
S3_BUCKET=my-policy-bucket cat examples/example-input.json | go run main.go <policy_name>
```

AWS credentials must be configured in your environment or `~/.aws/credentials`.

### Building

```sh
cd lambda
go build -o opa_lambda main.go
```

### Testing

```sh
cd lambda
go test ./...
```

## Project Structure

```
.
├── cloudformation/
│   └── opa-lambda-stack.yaml    # CloudFormation template
├── lambda/
│   ├── examples/
│   │   └── example-input.json   # Example test data
│   ├── policies/
│   │   ├── example.rego         # Example policy
│   │   └── world.rego           # Another example
│   ├── policyevaluator/         # Policy evaluation logic
│   ├── policyloader/            # Policy loading from S3/filesystem
│   └── main.go                  # Lambda entry point
└── README.md
```

## Contributing

Please submit issues and pull requests for any bug reports, feature requests, or other contributions.
