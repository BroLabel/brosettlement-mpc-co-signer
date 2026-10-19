require('reflect-metadata');
const { Module } = require('@nestjs/common');
const { NestFactory } = require('@nestjs/core');
const { FastifyAdapter } = require('@nestjs/platform-fastify');
const { SwaggerModule, DocumentBuilder } = require('@nestjs/swagger');
const { writeFileSync } = require('node:fs');
const { resolve } = require('node:path');
const { MpcStatusResponseDto } = require(resolve('apps/client-api/src/mpc/api/dto/mpc-status.response.dto.ts'));
const { OrganizationCoSignerStatusDto } = require(resolve('apps/admin-api/src/organizations/dto/organization-co-signer-status.dto.ts'));
class SchemaModule {}
Module({})(SchemaModule);
(async () => {
  const app = await NestFactory.create(SchemaModule, new FastifyAdapter(), { logger: false });
  try {
    for (const [name, dto] of [['client', MpcStatusResponseDto], ['admin', OrganizationCoSignerStatusDto]]) {
      const document = SwaggerModule.createDocument(app, new DocumentBuilder().setTitle(`Local ${name} status DTO schemas`).setVersion('1').build(), { extraModels: [dto] });
      const path = resolve(process.env.STATUS_SCHEMA_OUTPUT_DIR || process.cwd(), `${name}-status.openapi.json`);
      writeFileSync(path, JSON.stringify(document, null, 2) + '\n');
      console.log(path);
    }
  } finally { await app.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
