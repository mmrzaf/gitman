package admin

import (
	"fmt"

	"github.com/mmrzaf/gitman/internal/db"
)

func CreateUser(database *db.DB, username, password string) error {
	if err := ValidateUsername(username); err != nil {
		return err
	}
	if err := IsPasswordStrong(password); err != nil {
		return err
	}

	ctx := contextBackground()
	_, err := database.CreateUser(ctx, username, password)
	if err != nil {
		return fmt.Errorf("failed to create user: %w", err)
	}

	fmt.Printf("User %q created successfully.\n", username)
	return nil
}

func ResetPassword(database *db.DB, username, password string) error {
	if err := IsPasswordStrong(password); err != nil {
		return err
	}
	ctx := contextBackground()
	if err := database.UpdateUserPassword(ctx, username, password); err != nil {
		return fmt.Errorf("failed to reset password: %w", err)
	}

	fmt.Printf("Password for %q reset successfully.\n", username)
	return nil
}

func DeleteUser(database *db.DB, username string) error {
	ctx := contextBackground()
	if err := database.DeleteUserByUsername(ctx, username); err != nil {
		return fmt.Errorf("failed to delete user: %w", err)
	}

	fmt.Printf("User %q deleted.\n", username)
	return nil
}
