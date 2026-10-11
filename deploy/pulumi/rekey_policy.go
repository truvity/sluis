package sluispulumi

import "fmt"

// RekeyOperatorStatements are the statements of the principal that runs
// `sluis secrets rekey`: an operator's role, never a function's. The command
// re-writes every SecureString parameter below the instance's root under the
// estate's secrets key (LambdaArgs.Keys.Secrets, the document's
// `secrets.kmsKeyId`), so the role needs:
//
//   - SluisRekeyList: ssm:DescribeParameters, which has no resource-level
//     permission (Resource "*"), to list the parameters and the key each is under;
//   - SluisRekeyParameters: ssm:GetParameters and ssm:PutParameter on the
//     instance's parameters only;
//   - SluisRekeyKey: kms:Encrypt, kms:Decrypt and kms:GenerateDataKey on the new
//     key, through SSM only and for the instance's parameters only, the grant the
//     functions have.
//
// Reading a parameter still under a customer-managed key other than the new one
// also needs kms:Decrypt on that key; a parameter on the AWS-managed `aws/ssm`
// key needs nothing more. The estate's key policy must admit the role (the
// account-root statement does, with this IAM policy). An audited run also needs
// [OperatorAuditStatements].
func RekeyOperatorStatements(region, account, instance, keyArn string) ([]map[string]any, error) {
	if region == "" || account == "" || instance == "" || keyArn == "" {
		return nil, fmt.Errorf("sluispulumi: RekeyOperatorStatements: region, account, instance and the key ARN are required")
	}
	key := secretsKeyStatement(keyArn, region, account, instance)
	key["Sid"] = "SluisRekeyKey"
	return []map[string]any{
		{"Sid": "SluisRekeyList", "Effect": "Allow", "Action": []string{"ssm:DescribeParameters"}, "Resource": "*"},
		{
			"Sid": "SluisRekeyParameters", "Effect": "Allow",
			"Action":   []string{ssmGetParameters, ssmPutParameter},
			"Resource": parameterArnsUnder(region, account, SSMRoot(instance)),
		},
		key,
	}, nil
}
