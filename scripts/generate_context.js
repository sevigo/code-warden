const fs = require('fs');
const path = require('path');

// Configuration
const allowedExtensions = new Set(['.go', '.md', '.yml', '.yaml']);
const excludedDirs = new Set(['.git', 'node_modules', 'bin', '.vscode', '.agent', 'venv']);
const excludedFiles = new Set(['go.sum', 'package-lock.json']);

function walkDir(dir, callback) {
    const files = fs.readdirSync(dir);

    files.forEach((file) => {
        const filePath = path.join(dir, file);
        const stat = fs.statSync(filePath);

        if (stat.isDirectory()) {
            if (!excludedDirs.has(file)) {
                walkDir(filePath, callback);
            }
        } else {
            const ext = path.extname(file).toLowerCase();
            if (allowedExtensions.has(ext) && !excludedFiles.has(file)) {
                callback(filePath);
            }
        }
    });
}

function generateContext(rootDir) {
    let output = '';

    walkDir(rootDir, (filePath) => {
        const relativePath = path.relative(rootDir, filePath);
        const content = fs.readFileSync(filePath, 'utf-8');

        output += `\n<!-- FILE: ${relativePath} -->\n`;
        output += content;
        output += `\n<!-- END FILE: ${relativePath} -->\n`;
    });

    return output;
}

if (require.main === module) {
    const rootDir = process.cwd();
    try {
        const context = generateContext(rootDir);
        console.log(context);
    } catch (e) {
        console.error("Error generating context:", e);
        process.exit(1);
    }
}
