-- name: CreateAnswer :one
INSERT INTO answers (
    question_id, answer, is_correct, explanation
) VALUES (
    $1, $2, $3, $4
)
RETURNING *;

-- name: GetAnswerByID :one
SELECT * FROM answers
WHERE id = $1 LIMIT 1;

-- name: ListAnswers :many
SELECT * FROM answers
ORDER BY created_at ASC;

-- name: ListAnswersByQuestionID :many
SELECT * FROM answers
WHERE question_id = $1
ORDER BY created_at ASC; -- Or some other defined order

-- name: UpdateAnswer :one
UPDATE answers
SET
    question_id = $2,
    answer = $3,
    is_correct = $4,
    explanation = $5
WHERE id = $1
RETURNING *;

-- name: DeleteAnswer :exec
DELETE FROM answers
WHERE id = $1;

-- name: DeleteAnswersByQuestionID :exec
DELETE FROM answers
WHERE question_id = $1;

-- name: GetAnswerCorrectness :one
SELECT is_correct
FROM answers
WHERE id = $1;