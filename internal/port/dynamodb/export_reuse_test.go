package dynamodb

// ReuseCost lets the LocalStack run (package dynamodb_test) hold the reuse
// path to the same numbers as the fake.
var ReuseCost = reuseCost

// ConformanceFamilies lets the LocalStack run hold each module's table to the
// same suite: the record family and the permanent family it may write.
var ConformanceFamilies = conformanceFamilies
