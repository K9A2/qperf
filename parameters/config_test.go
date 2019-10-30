package parameters

import "testing"

func TestConfiguration_LoadConfigurationFile(t *testing.T) {
  var config TestConfig
  config.LoadConfigurationFile("./config.json")
}
