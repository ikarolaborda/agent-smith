# NestJS — Selected Documentation

Source: context7 `/nestjs/docs.nestjs.com`

## Controllers, Modules, and Providers — The Triad

NestJS applications are structured around three core constructs:
- **Modules** declare a cohesive feature unit and group controllers + providers.
- **Controllers** handle incoming HTTP routes and return responses.
- **Providers** (services, repositories, factories) carry business logic and are injectable via the IoC container.

## HTTP Controller with Decorators

```typescript
import {
  Controller, Get, Post, Put, Delete,
  Body, Param, Query, HttpCode, HttpStatus,
  ParseIntPipe, NotFoundException,
} from '@nestjs/common';
import { CatsService } from './cats.service';
import { CreateCatDto } from './dto/create-cat.dto';

@Controller('cats')
export class CatsController {
  constructor(private readonly catsService: CatsService) {}

  @Get()
  findAll(@Query('age') age?: number, @Query('breed') breed?: string) {
    return this.catsService.findAll({ age, breed });
  }

  @Get(':id')
  async findOne(@Param('id', ParseIntPipe) id: number) {
    const cat = await this.catsService.findOne(id);
    if (!cat) throw new NotFoundException(`Cat #${id} not found`);
    return cat;
  }

  @Post()
  create(@Body() createCatDto: CreateCatDto) {
    return this.catsService.create(createCatDto);
  }

  @Delete(':id')
  @HttpCode(HttpStatus.NO_CONTENT)
  remove(@Param('id', ParseIntPipe) id: number) {
    return this.catsService.remove(id);
  }
}
```

## Constructor-based Dependency Injection

```typescript
import { Controller, Get } from '@nestjs/common';
import { CatsService } from './cats.service';
import { Cat } from './interfaces/cat.interface';

@Controller('cats')
export class CatsController {
  constructor(private catsService: CatsService) {}

  @Get()
  async findAll(): Promise<Cat[]> {
    return this.catsService.findAll();
  }
}
```

## Registering Providers in a Module

```typescript
import { Module } from '@nestjs/common';
import { CatsController } from './cats/cats.controller';
import { CatsService } from './cats/cats.service';

@Module({
  controllers: [CatsController],
  providers: [CatsService],
})
export class AppModule {}
```

## Custom Decorators and Role Guards

```typescript
// common/decorators/user.decorator.ts
import { createParamDecorator, ExecutionContext } from '@nestjs/common';

export const CurrentUser = createParamDecorator(
  (field: string | undefined, ctx: ExecutionContext) => {
    const request = ctx.switchToHttp().getRequest();
    const user = request.user;
    return field ? user?.[field] : user;
  },
);

// Roles decorator using SetMetadata
import { SetMetadata } from '@nestjs/common';
export const ROLES_KEY = 'roles';
export const Roles = (...roles: string[]) => SetMetadata(ROLES_KEY, roles);

// Role guard reading the metadata
@Injectable()
export class RolesGuard implements CanActivate {
  constructor(private reflector: Reflector) {}

  canActivate(context: ExecutionContext): boolean {
    const requiredRoles = this.reflector.getAllAndOverride<string[]>(
      ROLES_KEY,
      [context.getHandler(), context.getClass()],
    );
    if (!requiredRoles) return true;
    const { user } = context.switchToHttp().getRequest();
    return requiredRoles.some((role) => user.roles?.includes(role));
  }
}

// Usage on a controller
@Roles('admin')
@UseGuards(AuthGuard, RolesGuard)
@Delete(':id')
remove(@Param('id') id: string) { /* ... */ }
```

## Pipes, Guards, Interceptors, Filters, Middleware

- **Pipes** transform or validate inputs. Built-in: `ValidationPipe`, `ParseIntPipe`, `ParseUUIDPipe`. Custom pipes implement `PipeTransform`.
- **Guards** determine if a request is allowed to proceed. Implement `CanActivate`. Run before route handlers, after middleware.
- **Interceptors** wrap method calls; can transform results, retry, cache, log. Implement `NestInterceptor`.
- **Exception filters** translate thrown errors into HTTP responses. Implement `ExceptionFilter`.
- **Middleware** runs before guards; closer to Express middleware semantics.

## Validation Pipe with class-validator

```typescript
// main.ts
app.useGlobalPipes(new ValidationPipe({ whitelist: true, forbidNonWhitelisted: true }));

// dto/create-cat.dto.ts
import { IsString, IsInt, Min, Max } from 'class-validator';

export class CreateCatDto {
  @IsString() name!: string;
  @IsInt() @Min(0) @Max(40) age!: number;
  @IsString() breed!: string;
}
```

## Configuration and Environment

- Use `@nestjs/config` and a per-environment `.env` file.
- Validate config at boot using `Joi.object({ ... })` or `class-validator`.

## Testing

```typescript
import { Test, TestingModule } from '@nestjs/testing';
import { CatsController } from './cats.controller';
import { CatsService } from './cats.service';

describe('CatsController', () => {
  let controller: CatsController;

  beforeEach(async () => {
    const module: TestingModule = await Test.createTestingModule({
      controllers: [CatsController],
      providers: [CatsService],
    }).compile();
    controller = module.get(CatsController);
  });

  it('returns a list', async () => {
    expect(await controller.findAll()).toBeInstanceOf(Array);
  });
});
```

## Best Practices

- One module per bounded feature; keep modules thin and explicit about imports/exports.
- Inject by interface where possible; use custom providers with tokens (`useClass`, `useFactory`).
- Prefer DTO classes + ValidationPipe for inbound payloads.
- Use guards for cross-cutting access control; keep auth logic out of controllers.
- Use interceptors for response shaping (e.g., wrapping in `{ data: ... }`).
- Test units in isolation; mock providers via `useValue` in `TestingModule`.
