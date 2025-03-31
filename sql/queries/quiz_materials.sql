-- name: LinkQuizMaterial :one
INSERT INTO quiz_materials (
    quiz_id, material_id
) VALUES (
    $1, $2
)
RETURNING *;

-- name: GetQuizMaterialByID :one
-- Less common to fetch by its own ID, but included for completeness
SELECT * FROM quiz_materials
WHERE id = $1 LIMIT 1;

-- name: GetQuizMaterialByQuizAndMaterialID :one
SELECT * FROM quiz_materials
WHERE quiz_id = $1 AND material_id = $2 LIMIT 1;

-- name: ListQuizMaterialsByQuizID :many
SELECT * FROM quiz_materials
WHERE quiz_id = $1;

-- name: ListMaterialIDsByQuizID :many
SELECT material_id FROM quiz_materials
WHERE quiz_id = $1;

-- name: ListQuizIDsByMaterialID :many
SELECT quiz_id FROM quiz_materials
WHERE material_id = $1;

-- name: UnlinkQuizMaterial :exec
DELETE FROM quiz_materials
WHERE quiz_id = $1 AND material_id = $2;

-- name: UnlinkAllMaterialsFromQuiz :exec
DELETE FROM quiz_materials
WHERE quiz_id = $1;

-- name: UnlinkMaterialFromAllQuizes :exec
DELETE FROM quiz_materials
WHERE material_id = $1;