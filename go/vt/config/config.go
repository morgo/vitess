/*
Copyright 2025 The Vitess Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package config provides functionality for Vitess components to read and use
// configuration from the vitess.yaml file.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"

	"vitess.io/vitess/go/vt/log"
	"vitess.io/vitess/go/vt/servenv"
	"vitess.io/vitess/go/vt/vterrors"
)

const (
	// DefaultConfigFile is the default location for the Vitess configuration file
	DefaultConfigFile = "/etc/vitess.yaml"
	// FallbackConfigFile is used if DefaultConfigFile is not found
	FallbackConfigFile = "./vitess.yaml"
)

var (
	// ConfigFile is the path to the configuration file
	ConfigFile string
	
	// viperInstance is the shared viper instance for reading config
	viperInstance *viper.Viper
)

// RegisterFlags registers the flags for the configuration file
func RegisterFlags(fs *pflag.FlagSet) {
	fs.StringVar(&ConfigFile, "vitess-config-file", DefaultConfigFile, "Path to the Vitess configuration file")
}

// Init initializes the configuration system
func Init() error {
	v := viper.New()
	v.SetConfigFile(ConfigFile)
	
	// Determine the config type based on file extension
	ext := strings.ToLower(filepath.Ext(ConfigFile))
	switch ext {
	case ".yaml", ".yml":
		v.SetConfigType("yaml")
		log.Infof("Setting config type to 'yaml' for file with extension: %s", ext)
	case ".cnf", ".ini":
		v.SetConfigType("ini")
		log.Infof("Setting config type to 'ini' for file with extension: %s", ext)
	case ".json":
		v.SetConfigType("json")
		log.Infof("Setting config type to 'json' for file with extension: %s", ext)
	case ".toml":
		v.SetConfigType("toml")
		log.Infof("Setting config type to 'toml' for file with extension: %s", ext)
	default:
		log.Infof("Using default config type detection for file with extension: %s", ext)
		// Let viper try to determine the type
	}
	
	// Check if the config file exists
	if _, err := os.Stat(ConfigFile); os.IsNotExist(err) {
		// If the default config file doesn't exist, try the fallback
		if ConfigFile == DefaultConfigFile {
			log.Infof("Configuration file %s not found, trying fallback %s", ConfigFile, FallbackConfigFile)
			ConfigFile = FallbackConfigFile
			v.SetConfigFile(ConfigFile)
			
			// Set the config type for the fallback file
			ext := strings.ToLower(filepath.Ext(ConfigFile))
			switch ext {
			case ".yaml", ".yml":
				v.SetConfigType("yaml")
			case ".cnf", ".ini":
				v.SetConfigType("ini")
			case ".json":
				v.SetConfigType("json")
			case ".toml":
				v.SetConfigType("toml")
			}
			
			// Check if the fallback config file exists
			if _, err := os.Stat(ConfigFile); os.IsNotExist(err) {
				log.Warningf("Fallback configuration file %s not found, using defaults and command-line flags", ConfigFile)
				viperInstance = v
				return nil
			}
		} else {
			log.Warningf("Configuration file %s not found, using defaults and command-line flags", ConfigFile)
			viperInstance = v
			return nil
		}
	}
	
	// Read the config file
	if err := v.ReadInConfig(); err != nil {
		// Print the file content for debugging
		content, readErr := os.ReadFile(ConfigFile)
		if readErr == nil {
			log.Infof("Content of %s:\n%s", ConfigFile, string(content))
		}
		return vterrors.Wrapf(err, "failed to read configuration file %s", ConfigFile)
	}
	
	log.Infof("Using configuration file: %s", ConfigFile)
	log.Infof("Configuration keys: %v", v.AllKeys())
	viperInstance = v
	return nil
}

// GetString gets a string value from the configuration file
func GetString(section, key, defaultValue string) string {
	if viperInstance == nil {
		return defaultValue
	}
	
	// Try section.key format
	value := viperInstance.GetString(fmt.Sprintf("%s.%s", section, key))
	if value != "" {
		return value
	}
	
	// Try direct key lookup
	value = viperInstance.GetString(key)
	if value != "" {
		return value
	}
	
	return defaultValue
}

// GetInt gets an integer value from the configuration file
func GetInt(section, key string, defaultValue int) int {
	if viperInstance == nil {
		return defaultValue
	}
	
	// Try section.key format
	value := viperInstance.GetInt(fmt.Sprintf("%s.%s", section, key))
	if value != 0 {
		return value
	}
	
	// Try direct key lookup
	value = viperInstance.GetInt(key)
	if value != 0 {
		return value
	}
	
	return defaultValue
}

// GetBool gets a boolean value from the configuration file
func GetBool(section, key string, defaultValue bool) bool {
	if viperInstance == nil {
		return defaultValue
	}
	
	// Try section.key format
	sectionKey := fmt.Sprintf("%s.%s", section, key)
	if viperInstance.IsSet(sectionKey) {
		return viperInstance.GetBool(sectionKey)
	}
	
	// Try direct key lookup
	if viperInstance.IsSet(key) {
		return viperInstance.GetBool(key)
	}
	
	return defaultValue
}

// GetSections returns all sections that match a given prefix
func GetSections(prefix string) []string {
	if viperInstance == nil {
		return []string{}
	}
	
	allSettings := viperInstance.AllSettings()
	sections := []string{}
	
	for section := range allSettings {
		if strings.HasPrefix(section, prefix) {
			sections = append(sections, section)
		}
	}
	
	return sections
}

// GetSectionMap returns a map of all key-value pairs in a section
func GetSectionMap(section string) map[string]interface{} {
	if viperInstance == nil {
		return map[string]interface{}{}
	}
	
	return viperInstance.GetStringMap(section)
}

func init() {
	servenv.OnParse(RegisterFlags)
}