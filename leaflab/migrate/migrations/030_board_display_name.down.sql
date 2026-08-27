-- Migration 030 down: reverse the household-chosen board display name column.

ALTER TABLE board DROP COLUMN IF EXISTS display_name;
