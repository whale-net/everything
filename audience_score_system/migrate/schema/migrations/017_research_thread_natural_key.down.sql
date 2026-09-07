-- Reverse migration 017: drop the natural-key unique index. Structural
-- reversibility only -- no data loss on reversal, the index carries no
-- rows of its own.

DROP INDEX research_thread_natural_key;
