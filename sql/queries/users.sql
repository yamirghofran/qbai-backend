-- name: CreateUser :one
INSERT INTO users (
    google_id, email, name, picture -- Added picture
) VALUES (
    $1, $2, $3, $4 -- Added $4 for picture
)
RETURNING *;

-- name: GetUserByID :one
SELECT * FROM users
WHERE id = $1 LIMIT 1;

-- name: GetUserByEmail :one
SELECT * FROM users
WHERE email = $1 LIMIT 1;

-- name: GetUserByGoogleID :one
SELECT * FROM users
WHERE google_id = $1 LIMIT 1;

-- name: ListUsers :many
SELECT * FROM users
ORDER BY created_at DESC; -- Or any other order

-- name: UpdateUser :one
UPDATE users
SET
    google_id = $2,
    email = $3,
    name = $4
WHERE id = $1
RETURNING *;

-- name: DeleteUser :exec
DELETE FROM users
WHERE id = $1;