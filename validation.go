package main

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

func validateUpdate(plainUpdate string) error {
	splitUpdate := strings.Split(plainUpdate, delimiter)

	fmt.Println("Validate update", plainUpdate)

	for _, item := range splitUpdate {
		if !validateString(item) {
			return errors.New("String validation failed for " + item)
		}
	}

	if sarpConfig.allow_new_team == true {
		if splitUpdate[0] != "team" || splitUpdate[12] != "team_alias" {
			return errors.New("Invalid team specified")
		}
		if !validateTeamId(splitUpdate[1]) {
			createNewTeam(splitUpdate[1], splitUpdate[13])
		} else if !validateTeamIdAndAlias(splitUpdate[1], splitUpdate[13]) {
			return errors.New("Invalid team specified")
		}
	} else {
		if splitUpdate[0] != "team" || !validateTeam(splitUpdate[1]) {
			return errors.New("Invalid team specified")
		}
	}

	if splitUpdate[2] != "image" || !validateImage(splitUpdate[3]) {
		return errors.New("Invalid image specified")
	}

	return nil
}

func validateString(input string) bool {
	if input == "" {
		return false
	}
	validationString := `^[a-zA-Z0-9-_]+$`
	inputValidation := regexp.MustCompile(validationString)
	return inputValidation.MatchString(input)
}

func validateTeam(teamName string) bool {
	for _, team := range sarpConfig.Team {
		if team.ID == teamName {
			return true
		}
		if team.Alias == teamName {
			return true
		}
	}
	return false
}

func validateTeamId(teamId string) bool {
	for _, team := range sarpConfig.Team {
		if team.ID == teamId {
			return true
		}
	}
	return false
}

func validateTeamIdAndAlias(teamId string, teamAlias string) bool {
	for _, team := range sarpConfig.Team {
		if team.ID == teamId && team.Alias == teamAlias {
			return true
		}
	}
	return false
}

func createNewTeam(teamId string, teamAlias string) {
	team := teamData{ID: teamId, Alias: teamAlias}
	sarpConfig.Team = append(sarpConfig.Team, team)
}

func validateImage(imageName string) bool {
	for _, image := range sarpConfig.Image {
		if image.Name == imageName {
			return true
		}
	}
	return false
}
