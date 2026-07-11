CREATE UNIQUE INDEX items_content_hash_unique ON items(content_hash) WHERE content_hash <> '';
