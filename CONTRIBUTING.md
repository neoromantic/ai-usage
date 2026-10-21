<!-- managed by devkit v1: edit in https://github.com/neoromantic/devkit/blob/main/templates/CONTRIBUTING.md -->
# Contributing

Thanks for helping. This is a small project with one maintainer, so short, focused pull requests
get merged fastest.

## Before you start

- For anything bigger than a small fix, open an issue first so we can agree on the approach.
- Read `AGENTS.md`: it holds the project rules for people and coding agents alike.

## Making a change

1. Fork the repo and create a branch from `main`.
2. Set up the project as described in the README.
3. Make the change, with tests. Tests run offline and use made-up data only.
4. Run the native checks, tests and build commands listed in `AGENTS.md` for the project.
5. Optional: run `lefthook install` once to get the shared pre-commit hooks (secret and privacy scan).
6. Open a pull request. Use a [Conventional Commits](https://www.conventionalcommits.org/) title,
   such as `fix: handle empty config`, unless `AGENTS.md` sets another commit style.

## What not to commit

Real names, phone numbers, tokens, session files, local paths or any personal data. CI scans for
secrets on every pull request.

## License

By contributing you agree that your work is released under the project's license.
