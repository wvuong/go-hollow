# fakehollowdata

This directory holds sample Hollow blobs (`snapshot-*`, `delta-*`, `reversedelta-*`,
`header-*`) used as test fixtures for the blob reader in `pkg/hollow`. The files
themselves are gitignored (generated binary data, ~500MB) — only this note is
checked in.

## Populating this directory

The blobs are produced by the `hollow-fakedata` module in the
[Netflix/hollow](https://github.com/Netflix/hollow) repo, which generates a fake
book-catalog dataset with a tunable number of producer cycles/states.

1. Clone `Netflix/hollow` (or use an existing local checkout).
2. Run `hollow.FakeDataGenerator#main` (`hollow-fakedata/src/main/java/hollow/FakeDataGenerator.java`)
   — e.g. from an IDE, or via Gradle once a run task is wired up for the module.
   It writes blobs to `/tmp/fakehollowdata` (the `BLOB_PATH` constant in that file)
   and then starts the Explorer/History UI servers, which block, so stop it with
   Ctrl+C once you see file output.
3. Copy the generated files into this directory:
   ```
   cp /tmp/fakehollowdata/* fakehollowdata/
   ```

Tunable parameters (dataset size, number of states, add/modify/remove entropy per
cycle) are constants at the top of `FakeDataGenerator.java`.
