const fs = require("fs");
const crypto = require("crypto");
const path = require("path");

function xorWithKey(data, key) {
    const keyLen = key.length;

    for (let i = 0, j = 0; i < data.length; i++, j++) {
        if (j >= keyLen) {
            j = 0;
        }

        data[i] = data[i] ^ key[j];
    }
}

function getInput(name, defaultValue = "") {
    const envName = "INPUT_" + name
        .replace(/ /g, "_")
        .replace(/-/g, "_")
        .toUpperCase();

    return process.env[envName] || defaultValue;
}

function setOutput(name, value) {
    const outputFile = process.env.GITHUB_OUTPUT;

    if (outputFile) {
        fs.appendFileSync(
            outputFile,
            `${name}=${value}${require("os").EOL}`
        );
    } else {
        console.log(`${name}: ${value}`);
    }
}

function byteCount(str) {
    return new TextEncoder().encode(str).length;
}

async function main() {
    const input = getInput("input");
    const output = getInput("output", "encrypted.bin");
    const keyLength = Number(getInput("key-length", "32"));

    if (!input) {
        throw new Error("Input binary is required");
    }

    if (!fs.existsSync(input)) {
        throw new Error(`Input file does not exist: ${input}`);
    }

    if (!Number.isInteger(keyLength) || keyLength <= 0) {
        throw new Error("key-length must be a positive integer");
    }

    const key = crypto.randomBytes(keyLength);
    const data = fs.readFileSync(input);

    xorWithKey(data, key);

    fs.mkdirSync(path.dirname(output), {
        recursive: true
    });

    fs.writeFileSync(output, data);

    const keyFile = `${output}.key`;
    fs.writeFileSync(keyFile, key);

    // Mostrar información
    console.log(`XOR key: ${key}`);
    console.log(`Encrypted binary: ${output}`);
    console.log(`Key file: ${keyFile}`);

    // Outputs de la Action
    setOutput("key length", byteCount(key));
    setOutput("encrypted-file", output);
}

main().catch(error => {
    console.error(error);
    process.exit(1);
});