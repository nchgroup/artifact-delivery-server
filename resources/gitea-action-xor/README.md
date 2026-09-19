# action-xor
XOR Encrypt File

## Usage

```yaml
      - name: Checkout Action XOR
        uses: actions/checkout@v4
        with:
          repository: admin/action-xor
          path: ./.gitea/actions/action-xor
          fetch-depth: 1

      - name: XOR encrypt
        id: xor
        uses: ./.gitea/actions/action-xor
        with:
          input: ./Rubeus/bin/Release/Rubeus.exe
          output: ./Rubeus.xor
          key-length: 32
```