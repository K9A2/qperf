package result

import (
  "encoding/json"
  "fmt"
  "io/ioutil"
)

type EvaluationResult struct {
  ClientResults []ConnectionResult
}

func WriteToJsonFile(fileName string, result EvaluationResult) {
  file, err := json.MarshalIndent(result, "", "  ")
  if err != nil {
    fmt.Printf("error in parsing evaluation result to json, err: %s\n", err.Error())
    return
  }
  err = ioutil.WriteFile(fileName, file, 0644)
  if err != nil {
    fmt.Printf("error in writing json file to: %s, error: %s\n", fileName, err.Error())
  }
}
