package transformers

import (
	"errors"
	"fmt"
	"strings"

	"github.com/benthosdev/benthos/v4/public/bloblang"
	"github.com/google/uuid"
	transformers_dataset "github.com/nucleuscloud/neosync/worker/internal/benthos/transformers/data-sets"
	transformer_utils "github.com/nucleuscloud/neosync/worker/internal/benthos/transformers/utils"
	"github.com/nucleuscloud/neosync/worker/internal/rng"
)

type generateEmailType string

const (
	uuidV4EmailType   generateEmailType = "uuidv4"
	fullNameEmailType generateEmailType = "fullname"
	anyEmailType      generateEmailType = "any"
)

func (g generateEmailType) String() string {
	return string(g)
}

func isValidEmailType(emailType string) bool {
	return emailType == string(uuidV4EmailType) || emailType == string(fullNameEmailType)
}

func init() {
	spec := bloblang.NewPluginSpec().
		Param(bloblang.NewInt64Param("max_length").Default(100000)).
		Param(bloblang.NewStringParam("email_type").Default(fullNameEmailType.String())).
		Param(bloblang.NewInt64Param("seed").Optional())

	err := bloblang.RegisterFunctionV2("generate_email", spec, func(args *bloblang.ParsedParams) (bloblang.Function, error) {
		maxLength, err := args.GetInt64("max_length")
		if err != nil {
			return nil, err
		}
		emailTypeArg, err := args.GetString("email_type")
		if err != nil {
			return nil, err
		}
		emailType := getEmailTypeOrDefault(emailTypeArg)

		seedArg, err := args.GetOptionalInt64("seed")
		if err != nil {
			return nil, err
		}
		var seed int64
		if seedArg != nil {
			seed = *seedArg
		} else {
			// we want a bit more randomness here with generate_email so using something that isn't time based
			var err error
			seed, err = transformer_utils.GenerateCryptoSeed()
			if err != nil {
				return nil, err
			}
		}
		randomizer := rng.New(seed)

		var excludedDomains []string

		return func() (any, error) {
			output, err := generateRandomEmail(randomizer, maxLength, emailType, excludedDomains)
			if err != nil {
				return nil, fmt.Errorf("unable to run generate_email: %w", err)
			}
			return output, nil
		}, nil
	})

	if err != nil {
		panic(err)
	}
}

func getEmailTypeOrDefault(input string) generateEmailType {
	if isValidEmailType(input) {
		return generateEmailType(input)
	}
	return uuidV4EmailType
}

func getRandomEmailDomain(randomizer rng.Rand, maxLength int64, excludedDomains []string) (string, error) {
	return transformer_utils.GenerateStringFromCorpus(
		randomizer,
		transformers_dataset.EmailDomains,
		transformers_dataset.EmailDomainMap,
		transformers_dataset.EmailDomainIndices,
		nil,
		maxLength,
		excludedDomains,
	)
}

/* Generates an email in the format <username@domain.tld> such as jdoe@gmail.com */
func generateRandomEmail(randomizer rng.Rand, maxLength int64, emailType generateEmailType, excludedDomains []string) (string, error) {
	if emailType == anyEmailType {
		emailType = getRandomEmailType(randomizer)
	}
	if emailType == uuidV4EmailType {
		return generateUuidEmail(randomizer, maxLength, excludedDomains)
	}
	return generateFullnameEmail(randomizer, maxLength, excludedDomains)
}

func getRandomEmailType(randomizer rng.Rand) generateEmailType {
	randInt := randomizer.Intn(2)
	if randInt == 0 {
		return uuidV4EmailType
	}
	return fullNameEmailType
}

func generateFullnameEmail(randomizer rng.Rand, maxLength int64, excludedDomains []string) (string, error) {
	domainMaxLength := maxLength - 2 // is there enough room for at least one character and an @ sign
	if (domainMaxLength) <= 0 {
		return "", fmt.Errorf("for the given max length, unable to generate an email of sufficient length: %d", maxLength)
	}

	domain, err := getRandomEmailDomain(randomizer, domainMaxLength, excludedDomains)
	if err != nil {
		return "", err
	}

	fullNameMaxLength := maxLength - int64(len(domain)) - 1 // original full length, minus the computed domain, minus an @ sign

	generatename, err := generateNameForEmail(randomizer, nil, fullNameMaxLength)
	if err != nil {
		return "", fmt.Errorf("unable to generate name for email: %w", err)
	}
	return fmt.Sprintf("%s@%s", generatename, domain), nil
}

// Generates a full name for an email. This will generate ASCII only characters (no unicode)
// If the max length is constrictive, it may not be able to generate a full name.
// If it can't generate a full name, will generate a last name. If it can't, it will generate a random character string.
// Currently it can still hit failure conditions, if this proves difficult, it can be updated to try to not fail at all costs
func generateNameForEmail(randomizer rng.Rand, minLength *int64, maxLength int64) (string, error) {
	maxFirstNameIdx, maxLastNameIdx := transformer_utils.FindClosestPair(
		transformers_dataset.FirstNameIndices, transformers_dataset.LastNameIndices,
		maxLength,
	)

	var randomFirstName string
	var randomLastName string
	if maxFirstNameIdx == -1 && maxLastNameIdx == -1 {
		var err error
		randomLastName, err = generateRandomLastName(randomizer, minLength, maxLength)
		if err != nil {
			// we don't want to fail at any cost, so generate a random character string because we've been given a value we can't generate a last name for
			randomLastName = transformer_utils.GetRandomCharacterString(randomizer, maxLength)
		}
	}
	if maxFirstNameIdx != -1 {
		maxFirstNameLength := transformers_dataset.FirstNameIndices[maxFirstNameIdx]
		var err error
		randomFirstName, err = generateRandomFirstName(randomizer, nil, maxFirstNameLength)
		if err != nil {
			return "", err
		}
	}
	if maxLastNameIdx != -1 {
		maxLastNameLength := transformers_dataset.LastNameIndices[maxLastNameIdx]
		var err error
		randomLastName, err = generateRandomLastName(randomizer, nil, maxLastNameLength)
		if err != nil {
			return "", err
		}
	}

	randomFirstName = strings.ToLower(transformer_utils.WithoutCharacters(randomFirstName, transformer_utils.SpecialChars))
	randomLastName = strings.ToLower(transformer_utils.WithoutCharacters(randomLastName, transformer_utils.SpecialChars))

	if randomFirstName == "" && randomLastName == "" {
		return "", errors.New("unable to generate random first and/or last name for email")
	}

	pieces := []string{}
	if randomFirstName != "" {
		pieces = append(pieces, randomFirstName)
	}
	if randomLastName != "" {
		pieces = append(pieces, randomLastName)
	}

	fullname := strings.Join(pieces, "")
	if minLength != nil && int64(len(fullname)) < *minLength {
		delta := *minLength - int64(len(fullname))
		fullname += transformer_utils.GetRandomCharacterString(randomizer, delta)
	}
	return fullname, nil
}

func generateUuidEmail(randomizer rng.Rand, maxLength int64, excludedDomains []string) (string, error) {
	domainMaxLength := maxLength - 2 // is there enough room for at least one character and an @ sign
	if (domainMaxLength) <= 0 {
		return "", fmt.Errorf("for the given max length, unable to generate an email of sufficient length: %d", maxLength)
	}
	domain, err := getRandomEmailDomain(randomizer, domainMaxLength, excludedDomains)
	if err != nil {
		return "", fmt.Errorf("unable to generate random email domain given the max length when generating a uuid email: %d", maxLength)
	}
	newuuid := strings.ReplaceAll(uuid.NewString(), "-", "")
	trimmedUuid := transformer_utils.TrimStringIfExceeds(newuuid, maxLength-int64(len(domain))-1)
	if trimmedUuid == "" { // todo: if this doesn't work, we should try with a different email domain to see if there is one that works. Maybe we could use the closest pair algorithm to find this
		return "", fmt.Errorf("for the given max length, unable to use a uuid to generate an email for the given length: %d", maxLength)
	}

	return fmt.Sprintf("%s@%s", trimmedUuid, domain), nil
}
