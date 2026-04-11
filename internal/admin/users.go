package admin

import (
	"context"
	"fmt"

	"github.com/mmrzaf/gitman/internal/db"
)

func CreateUser(database *db.DB, username, password string) error {
	if err := validateUser(username, password); err != nil {
		return err
	}

	user, err := database.CreateUser(context.Background(), username, password)
	if err != nil {
		return err
	}

	fmt.Printf("created user %s (id=%s)\n", user.Username, user.ID)

	return nil
}

func ResetPassword(database *db.DB, username, password string) error {
	if len(password) < 8 {
		return fmt.Errorf("password must be at least 8 characters")
	}

	err := database.UpdateUserPassword(context.Background(), username, password)
	if err != nil {
		return err
	}

	fmt.Println("password updated")

	return nil
}

func DeleteUser(database *db.DB, username string) error {
	err := database.DeleteUserByUsername(context.Background(), username)
	if err != nil {
		return err
	}

	fmt.Println("user deleted")

	return nil
}
