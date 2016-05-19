package conf

import (
	"encoding/json"
	"os"
)

// Configuration holds all the confiruation for authlify
type Configuration struct {
	SiteURL string `json:"site_url"`

	JWT struct {
		Secret string `json:"secret"`
	} `json:"jwt"`

	DB struct {
		Driver  string `json:"driver"`
		ConnURL string `json:"url"`
	}

	API struct {
		Host string `json:"host"`
		Port int    `json:"port"`
	} `json:"api"`

	Mailer struct {
		Host           string `json:"host"`
		Port           int    `json:"port"`
		User           string `json:"user"`
		Pass           string `json:"pass"`
		TemplateFolder string `json:"template_folder"`
		AdminEmail     string `json:"admin_email"`
		MailSubjects   struct {
			OrderMail string `json:"confirmation"`
		} `json:"mail_subjects"`
	} `json:"mailer"`

	Payment struct {
		Stripe struct {
			SecretKey string `json:"secret_key"`
		} `json:"stripe"`
		Paypal struct {
		} `json:"paypal"`
	} `json:"payment"`
}

// Load will construct the config from the file `config.json`
func Load() (*Configuration, error) {
	return LoadWithFile("config.json")
}

// LoadWithFile constructs the config from the specified file
func LoadWithFile(filePath string) (*Configuration, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}

	decoder := json.NewDecoder(file)

	var conf Configuration
	if err := decoder.Decode(&conf); err != nil {
		return nil, err
	}

	return &conf, nil
}
