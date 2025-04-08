-- +goose Up
-- SQL in this section is executed when the migration is applied.

-- Enable UUID generation
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- Function to automatically update 'updated_at' timestamp
-- Function to automatically update 'updated_at' timestamp
CREATE OR REPLACE FUNCTION trigger_set_timestamp() RETURNS TRIGGER AS 'BEGIN NEW.updated_at = NOW(); RETURN NEW; END;' LANGUAGE plpgsql;

-- ENUM Types
CREATE TYPE token_type AS ENUM ('purchase', 'gift', 'usage', 'refund', 'bonus', 'subscription_grant');
CREATE TYPE quiz_visibility AS ENUM ('public', 'private', 'unlisted');
CREATE TYPE activity_action AS ENUM (
    'login',
    'logout',
    'quiz_create',
    'quiz_update',
    'quiz_delete',
    'quiz_attempt_start',
    'quiz_attempt_finish',
    'quiz_share',
    'quiz_comment',
    'quiz_like',
    'quiz_bookmark',
    'material_upload',
    'topic_create',
    'topic_update',
    'topic_delete',
    'token_purchase',
    'subscription_update',
    'error',
    'feedback_create'
);
CREATE TYPE activity_target_type AS ENUM ('quiz', 'question', 'answer', 'topic', 'material', 'user', 'quiz_attempt', 'subscription', 'purchase', 'feedback');

-- sessions Table
CREATE TABLE IF NOT EXISTS sessions (
    token TEXT PRIMARY KEY,
    data BYTEA NOT NULL,
    expiry TIMESTAMPTZ NOT NULL
);

-- Index on sessions table
CREATE INDEX IF NOT EXISTS sessions_expiry_idx ON sessions (expiry);

-- users Table
CREATE TABLE users (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    google_id TEXT UNIQUE,
    email TEXT UNIQUE NOT NULL,
    name TEXT,
    picture TEXT,
    input_tokens_balance INT NOT NULL DEFAULT 500000,
    output_tokens_balance INT NOT NULL DEFAULT 50000,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- Trigger for users updated_at
CREATE TRIGGER set_timestamp_users
BEFORE UPDATE ON users
FOR EACH ROW
EXECUTE FUNCTION trigger_set_timestamp();
-- Index on email and google_id for faster lookups
CREATE INDEX idx_users_email ON users(email);
CREATE INDEX idx_users_google_id ON users(google_id);


-- tokens Table
CREATE TABLE tokens (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    amount INTEGER NOT NULL,
    type token_type NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- Trigger for tokens updated_at
CREATE TRIGGER set_timestamp_tokens
BEFORE UPDATE ON tokens
FOR EACH ROW
EXECUTE FUNCTION trigger_set_timestamp();
-- Index on user_id
CREATE INDEX idx_tokens_user_id ON tokens(user_id);


-- quizes Table
CREATE TABLE quizes (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    creator_id UUID REFERENCES users(id) ON DELETE SET NULL,
    title TEXT NOT NULL,
    description TEXT,
    visibility quiz_visibility NOT NULL DEFAULT 'private',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- Trigger for quizes updated_at
CREATE TRIGGER set_timestamp_quizes
BEFORE UPDATE ON quizes
FOR EACH ROW
EXECUTE FUNCTION trigger_set_timestamp();
-- Index on creator_id and visibility
CREATE INDEX idx_quizes_creator_id ON quizes(creator_id);
CREATE INDEX idx_quizes_visibility ON quizes(visibility);


-- materials Table
CREATE TABLE materials (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    url TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- Trigger for materials updated_at
CREATE TRIGGER set_timestamp_materials
BEFORE UPDATE ON materials
FOR EACH ROW
EXECUTE FUNCTION trigger_set_timestamp();
-- Index on user_id
CREATE INDEX idx_materials_user_id ON materials(user_id);


-- quiz_materials Join Table
CREATE TABLE quiz_materials (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    quiz_id UUID NOT NULL REFERENCES quizes(id) ON DELETE CASCADE,
    material_id UUID NOT NULL REFERENCES materials(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (quiz_id, material_id)
);
-- Trigger for quiz_materials updated_at
CREATE TRIGGER set_timestamp_quiz_materials
BEFORE UPDATE ON quiz_materials
FOR EACH ROW
EXECUTE FUNCTION trigger_set_timestamp();
-- Indexes for joining
CREATE INDEX idx_quiz_materials_quiz_id ON quiz_materials(quiz_id);
CREATE INDEX idx_quiz_materials_material_id ON quiz_materials(material_id);


-- topics Table
CREATE TABLE topics (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    creator_id UUID REFERENCES users(id) ON DELETE SET NULL,
    title TEXT NOT NULL,
    description TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- Trigger for topics updated_at
CREATE TRIGGER set_timestamp_topics
BEFORE UPDATE ON topics
FOR EACH ROW
EXECUTE FUNCTION trigger_set_timestamp();
-- Index on creator_id
CREATE INDEX idx_topics_creator_id ON topics(creator_id);


-- quiz_topics Join Table
CREATE TABLE quiz_topics (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    quiz_id UUID NOT NULL REFERENCES quizes(id) ON DELETE CASCADE,
    topic_id UUID NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (quiz_id, topic_id)
);
-- Trigger for quiz_topics updated_at
CREATE TRIGGER set_timestamp_quiz_topics
BEFORE UPDATE ON quiz_topics
FOR EACH ROW
EXECUTE FUNCTION trigger_set_timestamp();
-- Indexes for joining
CREATE INDEX idx_quiz_topics_quiz_id ON quiz_topics(quiz_id);
CREATE INDEX idx_quiz_topics_topic_id ON quiz_topics(topic_id);


-- questions Table
CREATE TABLE questions (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    quiz_id UUID NOT NULL REFERENCES quizes(id) ON DELETE CASCADE,
    topic_id UUID NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
    question TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- Trigger for questions updated_at
CREATE TRIGGER set_timestamp_questions
BEFORE UPDATE ON questions
FOR EACH ROW
EXECUTE FUNCTION trigger_set_timestamp();
-- Indexes
CREATE INDEX idx_questions_quiz_id ON questions(quiz_id);
CREATE INDEX idx_questions_topic_id ON questions(topic_id);


-- answers Table
CREATE TABLE answers (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    question_id UUID NOT NULL REFERENCES questions(id) ON DELETE CASCADE,
    answer TEXT NOT NULL,
    is_correct BOOLEAN NOT NULL DEFAULT FALSE,
    explanation TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- Trigger for answers updated_at
CREATE TRIGGER set_timestamp_answers
BEFORE UPDATE ON answers
FOR EACH ROW
EXECUTE FUNCTION trigger_set_timestamp();
-- Index on question_id
CREATE INDEX idx_answers_question_id ON answers(question_id);


-- quiz_attempts Table
CREATE TABLE quiz_attempts (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    quiz_id UUID NOT NULL REFERENCES quizes(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    score INTEGER,
    start_time TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    end_time TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- Trigger for quiz_attempts updated_at
CREATE TRIGGER set_timestamp_quiz_attempts
BEFORE UPDATE ON quiz_attempts
FOR EACH ROW
EXECUTE FUNCTION trigger_set_timestamp();
-- Indexes
CREATE INDEX idx_quiz_attempts_quiz_id ON quiz_attempts(quiz_id);
CREATE INDEX idx_quiz_attempts_user_id ON quiz_attempts(user_id);


-- attempt_answers Table
CREATE TABLE attempt_answers (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    quiz_attempt_id UUID NOT NULL REFERENCES quiz_attempts(id) ON DELETE CASCADE,
    question_id UUID NOT NULL REFERENCES questions(id) ON DELETE CASCADE,
    selected_answer_id UUID REFERENCES answers(id) ON DELETE SET NULL,
    is_correct BOOLEAN,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- Add unique constraint for upsert target
    UNIQUE (quiz_attempt_id, question_id)
);
-- Trigger for attempt_answers updated_at
CREATE TRIGGER set_timestamp_attempt_answers
BEFORE UPDATE ON attempt_answers
FOR EACH ROW
EXECUTE FUNCTION trigger_set_timestamp();
-- Indexes
CREATE INDEX idx_attempt_answers_quiz_attempt_id ON attempt_answers(quiz_attempt_id);
CREATE INDEX idx_attempt_answers_question_id ON attempt_answers(question_id);
CREATE INDEX idx_attempt_answers_selected_answer_id ON attempt_answers(selected_answer_id);


-- activity_logs Table
CREATE TABLE activity_logs (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    action activity_action NOT NULL,
    target_type activity_target_type,
    target_id UUID,
    details JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- Trigger for activity_logs updated_at
CREATE TRIGGER set_timestamp_activity_logs
BEFORE UPDATE ON activity_logs
FOR EACH ROW
EXECUTE FUNCTION trigger_set_timestamp();
-- Indexes
CREATE INDEX idx_activity_logs_user_id ON activity_logs(user_id);
CREATE INDEX idx_activity_logs_action ON activity_logs(action);
CREATE INDEX idx_activity_logs_target ON activity_logs(target_type, target_id);
CREATE INDEX idx_activity_logs_created_at ON activity_logs(created_at);



-- +goose Down
-- SQL in this section is executed when the migration is rolled back.

-- Drop triggers first
DROP TRIGGER IF EXISTS set_timestamp_users ON users;
DROP TRIGGER IF EXISTS set_timestamp_tokens ON tokens;
DROP TRIGGER IF EXISTS set_timestamp_quizes ON quizes;
DROP TRIGGER IF EXISTS set_timestamp_materials ON materials;
DROP TRIGGER IF EXISTS set_timestamp_quiz_materials ON quiz_materials;
DROP TRIGGER IF EXISTS set_timestamp_topics ON topics;
DROP TRIGGER IF EXISTS set_timestamp_quiz_topics ON quiz_topics;
DROP TRIGGER IF EXISTS set_timestamp_questions ON questions;
DROP TRIGGER IF EXISTS set_timestamp_answers ON answers;
DROP TRIGGER IF EXISTS set_timestamp_quiz_attempts ON quiz_attempts;
DROP TRIGGER IF EXISTS set_timestamp_attempt_answers ON attempt_answers;
DROP TRIGGER IF EXISTS set_timestamp_activity_logs ON activity_logs;

-- Drop tables in reverse order of creation
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS activity_logs;
DROP TABLE IF EXISTS attempt_answers;
DROP TABLE IF EXISTS quiz_attempts;
DROP TABLE IF EXISTS answers;
DROP TABLE IF EXISTS questions;
DROP TABLE IF EXISTS quiz_topics;
DROP TABLE IF EXISTS topics;
DROP TABLE IF EXISTS quiz_materials;
DROP TABLE IF EXISTS materials;
DROP TABLE IF EXISTS quizes;
DROP TABLE IF EXISTS tokens;
DROP TABLE IF EXISTS users;

-- Drop ENUM types in reverse order of creation
DROP TYPE IF EXISTS activity_target_type;
DROP TYPE IF EXISTS activity_action;
DROP TYPE IF EXISTS quiz_visibility;
DROP TYPE IF EXISTS token_type;

-- Drop the trigger function
DROP FUNCTION IF EXISTS trigger_set_timestamp();

