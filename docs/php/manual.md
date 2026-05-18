# PHP 8.5 — Selected Documentation

Source: context7 `/websites/php_net_manual_en`

## Enums with Traits and Interfaces

```php
<?php

interface Colorful
{
    public function color(): string;
}

trait Rectangle
{
    public function shape(): string
    {
        return "Rectangle";
    }
}

enum Suit implements Colorful
{
    use Rectangle;

    case Hearts;
    case Diamonds;
    case Clubs;
    case Spades;

    public function color(): string
    {
        return match($this) {
            Suit::Hearts, Suit::Diamonds => 'Red',
            Suit::Clubs, Suit::Spades => 'Black',
        };
    }
}
```

## PHP Attributes — Syntax

Attributes are declarative metadata attached to classes, methods, properties, and parameters using `#[...]` syntax. They can carry constructor arguments, named arguments, and constant expressions.

```php
<?php

namespace MyExample;

use Attribute;

#[Attribute]
class MyAttribute
{
    const VALUE = 'value';

    public function __construct(private mixed $value = null) {}
}

#[MyAttribute]
#[MyAttribute(1234)]
#[MyAttribute(value: 1234)]
#[MyAttribute(MyAttribute::VALUE)]
#[MyAttribute(['key' => 'value'])]
#[MyAttribute(100 + 200)]
class Thing {}

#[MyAttribute(1234), MyAttribute(5678)]
class AnotherThing {}
```

## Type Declarations

- Scalar types: `int`, `float`, `string`, `bool`.
- Compound types: `array`, `object`, `iterable`, `callable`.
- Special types: `mixed`, `void`, `never`, `null`, `false`, `true`.
- Union types: `int|string`.
- Intersection types: `Iterator&Countable`.
- Nullable: `?Foo` or `Foo|null`.

### Covariant Return Narrowing

```php
<?php

interface ITest
{
    public function apfel(): mixed; // valid as of PHP 8.0
}

class Test implements ITest
{
    public function apfel(): array // more specific
    {
        return [];
    }
}
```

## Function Definition

```
function name ( parameter type   parameter name ) : return type
```

## Migration Notes — PHP 8.0 → 8.5 (Selected Highlights)

- **Named Arguments** — pass arguments by parameter name; skip optional defaults.
- **Constructor Property Promotion** — declare and assign properties in `__construct()` signature.
- **Union Types** — `function foo(int|string $x)`.
- **Match Expression** — strict-comparison switch returning a value.
- **Nullsafe Operator** — `$user?->profile?->avatar`.
- **Attributes** — replace docblock annotations.
- **Readonly Properties** (PHP 8.1) — `public readonly string $id;` once-only assignment from inside the declaring class.
- **Enums** (PHP 8.1) — first-class enumerated types with cases, methods, traits, and interfaces; backed enums map cases to scalar values.
- **First-class Callable Syntax** (PHP 8.1) — `$fn = strlen(...);`, `$method = $obj->method(...);`.
- **never return type** (PHP 8.1) — for functions that always throw or exit.
- **readonly classes** (PHP 8.2) — every property is implicitly readonly.
- **`true`/`false`/`null` standalone types** (PHP 8.2).
- **Typed class constants** (PHP 8.3).
- **`#[\Override]` attribute** (PHP 8.3) — explicit override marker; engine verifies.
- **Property hooks** (PHP 8.4) — `public string $name { get => ...; set(...) {...} }`.
- **Asymmetric property visibility** (PHP 8.4) — `public private(set) string $id;`.

## OOP — Class, Interface, Trait, Abstract

```php
<?php

interface Repository
{
    public function find(int $id): ?Entity;
    public function save(Entity $entity): void;
}

abstract class BaseRepository implements Repository
{
    public function __construct(
        protected readonly PDO $pdo,
    ) {}

    abstract protected function table(): string;
}

trait Timestamped
{
    public ?\DateTimeImmutable $createdAt = null;
    public ?\DateTimeImmutable $updatedAt = null;
}

final class UserRepository extends BaseRepository
{
    use Timestamped;

    protected function table(): string
    {
        return 'users';
    }

    public function find(int $id): ?Entity
    {
        $stmt = $this->pdo->prepare(
            'SELECT * FROM ' . $this->table() . ' WHERE id = :id',
        );
        $stmt->execute(['id' => $id]);
        $row = $stmt->fetch(\PDO::FETCH_ASSOC);
        return $row === false ? null : Entity::fromArray($row);
    }

    public function save(Entity $entity): void
    {
        // ...
    }
}
```

## Best Practices (Highlights)

- Prefer typed properties everywhere; use `readonly` when mutation after construction is not needed.
- Prefer strict comparisons (`===`, `!==`).
- Use enums over loose string constants.
- Use first-class callable syntax over `Closure::fromCallable`.
- Use `#[\Override]` for methods overriding a parent.
- Prefer constructor property promotion for value objects and DTOs.
- Prefer early returns; avoid `else` after a return.
- Use `match` over chained `if/elseif` when matching discrete values.
- Avoid global state; inject dependencies through constructors.
