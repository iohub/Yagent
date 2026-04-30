#!/bin/bash
set -e
REPO="/tmp/repos/noncode_04_db_migration"
rm -rf "$REPO"
mkdir -p "$REPO/schemas"
cd "$REPO"

cat > schemas/old_schema.sql << 'EOF'
-- Legacy schema: single flat users table

CREATE TABLE users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    username VARCHAR(50) NOT NULL UNIQUE,
    email VARCHAR(100) NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    full_name VARCHAR(100),
    bio TEXT,
    avatar_url VARCHAR(500),
    role VARCHAR(200),  -- comma-separated: "admin,editor,viewer"
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_users_email ON users(email);
CREATE INDEX idx_users_username ON users(username);
EOF

cat > schemas/new_schema.sql << 'EOF'
-- New normalized schema with separate profiles and roles tables

CREATE TABLE users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    username VARCHAR(50) NOT NULL UNIQUE,
    email VARCHAR(100) NOT NULL UNIQUE,
    password_hash VARCHAR(255) NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE profiles (
    user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    full_name VARCHAR(100),
    bio TEXT,
    avatar_url VARCHAR(500)
);

CREATE TABLE roles (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name VARCHAR(50) NOT NULL UNIQUE,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE user_roles (
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role_id INTEGER NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    assigned_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, role_id)
);

CREATE INDEX idx_users_email ON users(email);
CREATE INDEX idx_users_username ON users(username);
CREATE INDEX idx_profiles_user_id ON profiles(user_id);
CREATE INDEX idx_user_roles_user_id ON user_roles(user_id);
CREATE INDEX idx_user_roles_role_id ON user_roles(role_id);
EOF

cat > seed_data.sql << 'EOF'
-- Sample data for old schema

INSERT INTO users (username, email, password_hash, full_name, bio, avatar_url, role) VALUES
('alice', 'alice@example.com', 'hash_alice_123', 'Alice Johnson', 'Software engineer and open source contributor', 'https://cdn.example.com/avatars/alice.jpg', 'admin,editor'),
('bob', 'bob@example.com', 'hash_bob_456', 'Bob Smith', 'DevOps specialist. Coffee addict.', 'https://cdn.example.com/avatars/bob.jpg', 'editor,viewer'),
('carol', 'carol@example.com', 'hash_carol_789', 'Carol Williams', 'Full-stack developer', 'https://cdn.example.com/avatars/carol.jpg', 'viewer'),
('dave', 'dave@example.com', 'hash_dave_012', 'Dave Brown', 'Data engineer passionate about ML', 'https://cdn.example.com/avatars/dave.jpg', 'editor'),
('eve', 'eve@example.com', 'hash_eve_345', 'Eve Davis', 'Security researcher', 'https://cdn.example.com/avatars/eve.jpg', 'admin'),
('frank', 'frank@example.com', 'hash_frank_678', 'Frank Miller', 'Product manager', NULL, 'viewer'),
('grace', 'grace@example.com', 'hash_grace_901', 'Grace Lee', 'UX designer', 'https://cdn.example.com/avatars/grace.jpg', 'viewer'),
('henry', 'henry@example.com', 'hash_henry_234', 'Henry Taylor', 'Backend engineer', NULL, 'editor,viewer'),
('iris', 'iris@example.com', 'hash_iris_567', 'Iris Anderson', 'Frontend developer', 'https://cdn.example.com/avatars/iris.jpg', 'viewer'),
('jack', 'jack@example.com', 'hash_jack_890', 'Jack Thomas', 'CTO, co-founder', 'https://cdn.example.com/avatars/jack.jpg', 'admin,editor,viewer');
EOF

cat > README.md << 'EOF'
# Database Migration Project

Migrating from a legacy flat `users` table to a normalized schema with separate `profiles` and `roles` tables.

## Current State (old_schema.sql)
- Single `users` table
- `role` field stores comma-separated role names
- Profile fields (`full_name`, `bio`, `avatar_url`) embedded in users table

## Target State (new_schema.sql)
- `users` table: core identity fields only
- `profiles` table: user profile data (1:1)
- `roles` table: role definitions
- `user_roles` table: many-to-many user-role assignments

## Data
See `seed_data.sql` for sample data in the old schema format.
EOF

echo "noncode_04_db_migration setup done"
