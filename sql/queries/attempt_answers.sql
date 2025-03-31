-- name: UpsertAttemptAnswer :one
INSERT INTO attempt_answers (quiz_attempt_id, question_id, selected_answer_id, is_correct)
VALUES ($1, $2, $3, $4)
ON CONFLICT (quiz_attempt_id, question_id)
DO UPDATE SET
    selected_answer_id = EXCLUDED.selected_answer_id,
    is_correct = EXCLUDED.is_correct,
    updated_at = NOW()
RETURNING *;

-- name: ListAttemptAnswersByAttempt :many
SELECT *
FROM attempt_answers
WHERE quiz_attempt_id = $1
ORDER BY created_at; -- Or order by question order if needed, requires joining questions

-- name: CalculateQuizAttemptScore :one
SELECT COUNT(*)
FROM attempt_answers
WHERE quiz_attempt_id = $1 AND is_correct = TRUE;

-- name: GetAttemptAnswer :one
SELECT *
FROM attempt_answers
WHERE quiz_attempt_id = $1 AND question_id = $2;